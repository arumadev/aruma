package core

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// Insert stores an object sent in one request (media or multipart): the body straight to a new
// key, the rules, then commitObject. length is the body's size, -1 when unknown.
//
// The write order is ADR-022 §5's: the file is durable at its new key before the transaction,
// and any refusal before COMMIT deletes it at once. The quota is reserved in storage.usage as the
// body arrives, all of it when the length is known, else 8 MiB at a time, and commitObject turns
// it into used bytes.
func (s *Service) Insert(ctx context.Context, b Bucket, p Principal, spec UploadSpec, body io.Reader, length int64) (Object, error) {
	if err := checkPre(spec.Surface, spec.Pre); err != nil {
		return Object{}, err
	}
	if err := validateName(spec.Name); err != nil {
		return Object{}, err
	}
	o, err := s.incomingObject(b, spec.Name, spec.Attrs, spec.Surface)
	if err != nil {
		return Object{}, err
	}
	switch {
	case length > s.limits.ObjectBytes || spec.Declared > s.limits.ObjectBytes:
		return Object{}, ErrTooLarge
	case spec.Declared >= 0 && length >= 0 && spec.Declared != length:
		return Object{}, ErrSizeMismatch
	}
	limit := s.limits.ObjectBytes
	if spec.Declared >= 0 {
		limit = spec.Declared
	}
	key := blob.ObjectKey(b.Project)
	w, err := s.store.Create(key)
	if err != nil {
		return Object{}, fmt.Errorf("%w: create %s: %w", ErrUnavailable, key, err)
	}
	res := &reservation{project: b.Project}
	h := newHasher()
	n, err := s.stream(ctx, writerAppend(w), body, h, length, limit, res)
	if err != nil {
		_ = w.Abort()
		s.releaseReservation(ctx, res)
		switch {
		case !errors.Is(err, errOverLimit):
		case spec.Declared >= 0:
			err = ErrSizeMismatch
		default:
			err = ErrTooLarge
		}
		return Object{}, err
	}
	hook("insert.written")
	if err := w.Commit(); err != nil {
		_ = w.Abort()
		return s.dropNew(ctx, key, res, fmt.Errorf("%w: commit %s: %w", ErrUnavailable, key, err))
	}
	hook("insert.durable")
	o.Size = n
	o.CRC32C, o.MD5 = h.sums()
	if spec.Declared >= 0 && n != spec.Declared {
		return s.dropNew(ctx, key, res, ErrSizeMismatch)
	}
	if err := checkChecksums(spec.Want, o.CRC32C, o.MD5); err != nil {
		return s.dropNew(ctx, key, res, err)
	}
	if err := s.decideCreate(ctx, b, p, &o, spec.Pre, spec.Surface); err != nil {
		return s.dropNew(ctx, key, res, err)
	}
	obj, err := s.commitObject(ctx, objectWrite{bucket: b, object: o, blobKey: key, pre: spec.Pre, reserved: res.n})
	switch {
	case errors.Is(err, errCommitUnknown):
		// A row may point to the file: it stays, and so does the reservation until Boot
		// reconciles the account.
		return Object{}, err
	case err != nil:
		return s.dropNew(ctx, key, res, err)
	}
	return obj, nil
}

// Rewrite copies an object to a new key, within one project (GCS rewriteTo), counted on the
// quota and the disk floor. The tokens go with it, sealed again for the destination's name,
// unless attrs replaces the metadata.
func (s *Service) Rewrite(ctx context.Context, src Bucket, srcName string, dst Bucket, dstName string, p Principal, attrs *Patch, pre Preconditions) (Object, error) {
	if err := checkPre(SurfaceGCS, pre); err != nil {
		return Object{}, err
	}
	if src.Project != dst.Project {
		return Object{}, ErrPermissionDenied
	}
	if err := validateName(dstName); err != nil {
		return Object{}, err
	}
	from, row, err := s.storedObject(ctx, src, srcName)
	if err != nil {
		return Object{}, err
	}
	if from == nil {
		return Object{}, ErrNotFound
	}
	if err := s.authorize(ctx, src, p, ruleOp{Method: rules.MethodGet, Name: srcName, Resource: from, Surface: SurfaceGCS}); err != nil {
		return Object{}, err
	}
	var o Object
	if attrs == nil {
		o = Object{Bucket: dst.Name, Name: dstName, ContentType: from.ContentType, ContentEncoding: from.ContentEncoding,
			ContentDisposition: from.ContentDisposition, ContentLanguage: from.ContentLanguage,
			CacheControl: from.CacheControl, Metadata: from.Metadata, DownloadTokens: from.DownloadTokens}
		applyDefaults(&o, SurfaceGCS)
	} else if o, err = s.incomingObject(dst, dstName, *attrs, SurfaceGCS); err != nil {
		return Object{}, err
	}
	r, err := s.store.Open(row.BlobKey)
	if err != nil {
		return Object{}, fmt.Errorf("%w: open %s: %w", ErrUnavailable, row.BlobKey, err)
	}
	defer r.Close()
	key := blob.ObjectKey(dst.Project)
	w, err := s.store.Create(key)
	if err != nil {
		return Object{}, fmt.Errorf("%w: create %s: %w", ErrUnavailable, key, err)
	}
	res := &reservation{project: dst.Project}
	h := newHasher()
	n, err := s.stream(ctx, writerAppend(w), r, h, row.Size, row.Size, res)
	if err != nil {
		_ = w.Abort()
		s.releaseReservation(ctx, res)
		return Object{}, err
	}
	if err := w.Commit(); err != nil {
		_ = w.Abort()
		return s.dropNew(ctx, key, res, fmt.Errorf("%w: commit %s: %w", ErrUnavailable, key, err))
	}
	o.Size = n
	o.CRC32C, o.MD5 = h.sums()
	if n != from.Size || o.CRC32C != from.CRC32C || o.MD5 != from.MD5 {
		s.log.Error("storage: an object's file does not match its row", "key", row.BlobKey)
		return s.dropNew(ctx, key, res, fmt.Errorf("%w: the source's file does not match its checksums", ErrUnavailable))
	}
	if err := s.decideCreate(ctx, dst, p, &o, pre, SurfaceGCS); err != nil {
		return s.dropNew(ctx, key, res, err)
	}
	obj, err := s.commitObject(ctx, objectWrite{bucket: dst, object: o, blobKey: key, pre: pre, reserved: res.n})
	switch {
	case errors.Is(err, errCommitUnknown):
		return Object{}, err
	case err != nil:
		return s.dropNew(ctx, key, res, err)
	}
	return obj, nil
}

// decideCreate is what a write checks before its commit, once the incoming object is complete:
// the preconditions on the current row (commitObject checks them again under the row's lock),
// then the rules, for which an overwrite is a create too (ADR-022 §8).
func (s *Service) decideCreate(ctx context.Context, b Bucket, p Principal, o *Object, pre Preconditions, sf Surface) error {
	existing, row, err := s.storedObject(ctx, b, o.Name)
	if err != nil {
		return err
	}
	if err := checkPreconditions(pre, row); err != nil {
		return err
	}
	return s.authorize(ctx, b, p, ruleOp{Method: rules.MethodCreate, Name: o.Name, Resource: existing, Incoming: o, Surface: sf})
}

// dropNew undoes a write refused before its COMMIT: its new file goes at once, and its
// reservation. A delete that fails is counted and logged; Boot's sweep deletes the file later.
func (s *Service) dropNew(ctx context.Context, key string, res *reservation, cause error) (Object, error) {
	if err := s.store.Delete(key); err != nil {
		s.metrics.DeleteFailures.Inc()
		s.log.Error("storage: delete a refused write's file", "key", key, "error", err)
	}
	s.releaseReservation(ctx, res)
	return Object{}, cause
}

// incomingObject is the object an upload's metadata describes, before its bytes: the content
// fields and custom metadata checked against the limits (ADR-022 §17), the defaults of the
// surface, and its download tokens: a new one on Firebase REST and the dashboard, the admin's
// own on GCS, none otherwise (ADR-022 §9).
func (s *Service) incomingObject(b Bucket, name string, attrs Patch, sf Surface) (Object, error) {
	o := Object{Bucket: b.Name, Name: name}
	for k, v := range attrs.Fields {
		if v == nil {
			continue
		}
		if err := s.checkHeaderField(k, *v); err != nil {
			return Object{}, err
		}
		switch k {
		case FieldContentType:
			o.ContentType = *v
		case FieldContentEncoding:
			o.ContentEncoding = *v
		case FieldContentDisposition:
			o.ContentDisposition = *v
		case FieldContentLanguage:
			o.ContentLanguage = *v
		case FieldCacheControl:
			o.CacheControl = *v
		default:
			return Object{}, invalid("Unknown metadata field " + k + ".")
		}
	}
	total := 0
	for k, v := range attrs.Metadata {
		if k == TokensKey {
			return Object{}, invalid("The custom metadata may not hold " + TokensKey + ".")
		}
		if v == nil {
			continue
		}
		if o.Metadata == nil {
			o.Metadata = map[string]string{}
		}
		o.Metadata[k] = *v
		total += len(k) + len(*v)
	}
	if total > s.limits.MetadataBytes {
		return Object{}, invalid("The custom metadata is larger than the limit.")
	}
	switch {
	case sf == SurfaceFirebase || sf == SurfaceConsole:
		o.DownloadTokens = []string{newDownloadToken()}
	case sf == SurfaceGCS && attrs.Tokens != nil:
		o.DownloadTokens = append([]string(nil), (*attrs.Tokens)...)
	}
	if _, err := s.sealTokens(b.Project, name, o.DownloadTokens); err != nil {
		return Object{}, err
	}
	applyDefaults(&o, sf)
	return o, nil
}

// checkHeaderField checks a content field, which becomes a response header: at most
// header_field_bytes, without CR, LF or other control characters.
func (s *Service) checkHeaderField(name, v string) error {
	if len(v) > s.limits.HeaderFieldBytes {
		return invalid("The field " + name + " is longer than the limit.")
	}
	for i := 0; i < len(v); i++ {
		if c := v[i]; c < 0x20 || c == 0x7f {
			return invalid("The field " + name + " may not contain control characters.")
		}
	}
	return nil
}

// reservation is the quota one request holds for the bytes it brings: n bytes in
// storage.usage, and in its session's row when it writes to one.
type reservation struct {
	project   int64
	anonymous bool
	session   []byte
	n         int64
}

// ensureReserved holds at least want bytes for the request, reserving the difference in one
// synchronous transaction.
func (s *Service) ensureReserved(ctx context.Context, r *reservation, want int64) error {
	more := want - r.n
	if more <= 0 {
		return nil
	}
	err := s.inTx(ctx, func(q *queries.Queries) error {
		if err := s.reserve(ctx, q, r.project, more, r.anonymous); err != nil {
			return err
		}
		if r.session == nil {
			return nil
		}
		rows, err := q.ReserveUploadSession(ctx, queries.ReserveUploadSessionParams{N: more, IDHash: r.session, ProjectNumber: r.project})
		if err != nil {
			return fmt.Errorf("%w: reserve: %w", ErrUnavailable, err)
		}
		if rows == 0 {
			return fmt.Errorf("%w: the upload session is no longer active", ErrUnavailable)
		}
		return nil
	})
	if err != nil {
		return err
	}
	r.n = want
	return nil
}

// releaseReservation gives back what a request without a session reserved. A failure is logged:
// the reservation then stays until Boot reconciles the account.
func (s *Service) releaseReservation(ctx context.Context, r *reservation) {
	if r.n == 0 {
		return
	}
	err := s.inTx(context.WithoutCancel(ctx), func(q *queries.Queries) error {
		return unreserve(ctx, q, r.project, r.n, r.anonymous)
	})
	if err != nil {
		s.log.Error("storage: give a reservation back", "error", err)
		return
	}
	r.n = 0
}

// appendFunc writes at most limit bytes of r and returns how many it wrote: a staging file's
// Append, or a new object file through writerAppend.
type appendFunc func(r io.Reader, limit int64) (int64, error)

func writerAppend(w io.Writer) appendFunc {
	return func(r io.Reader, limit int64) (int64, error) {
		n, err := io.CopyN(w, r, limit)
		if errors.Is(err, io.EOF) {
			err = nil
		}
		return n, err
	}
}

// errShortBody is a body that ends before the length its request stated.
var errShortBody = &InvalidError{Reason: "invalid", Message: "The request body is shorter than its length."}

// errOverLimit is a body that brings more than limit allows; the caller names it.
var errOverLimit = errors.New("storage: the body is over its limit")

// errStoreMismatch is a store that wrote other than what it read; nothing it wrote is announced.
var errStoreMismatch = errors.New("storage: the store wrote other than what it read")

// stream copies body through h to app: length bytes when known (-1 otherwise), never more than
// limit. Before each step it reserves the step's quota (ensureReserved) and its disk space
// (reserveDisk), all of a known length at once and 8 MiB at a time otherwise. It returns the
// bytes written; with an error, those bytes were read and hashed exactly, unless the error is
// errStoreMismatch. More than limit is errOverLimit, before anything is read when the length
// says so.
func (s *Service) stream(ctx context.Context, app appendFunc, body io.Reader, h *hasher, length, limit int64, res *reservation) (int64, error) {
	hr := &hashingReader{r: body, h: h}
	step := func(n int64) (int64, error) {
		// The request holds quota for every byte it wrote, and now for n more.
		if err := s.ensureReserved(ctx, res, hr.n+n); err != nil {
			return 0, err
		}
		if err := s.reserveDisk(n); err != nil {
			return 0, err
		}
		before := hr.n
		w, err := app(hr, n)
		s.releaseDisk(n)
		if w != hr.n-before {
			return w, errStoreMismatch
		}
		return w, err
	}
	if length >= 0 {
		if length > limit {
			return 0, errOverLimit
		}
		w, err := step(length)
		switch {
		case err != nil:
			return w, err
		case w < length:
			return w, errShortBody
		}
		return w, nil
	}
	var total int64
	for {
		n := min(int64(reserveStep), limit-total)
		if n == 0 {
			// At the limit: one more byte is over it.
			var probe [1]byte
			k, err := io.ReadFull(body, probe[:])
			if k > 0 {
				return total, errOverLimit
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return total, err
			}
			return total, nil
		}
		w, err := step(n)
		total += w
		if err != nil || w < n {
			return total, err
		}
	}
}
