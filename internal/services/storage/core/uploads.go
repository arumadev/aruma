package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// The upload session (ADR-022 §6, §7): one state machine under both protocols. A session is
// started (StartUpload), takes chunks at offsets (WriteUpload), is queried (QueryUpload), cancelled
// (CancelUpload), and finalized by its last chunk.
//
//   - The upload id is 128 random bits; only its HMAC is stored, and a session answers only on
//     the project and name it was started for. The id is the capability: the identity is the one
//     bound at start, and a chunk's own credential counts for nothing.
//   - One request at a time on a session: an in-process lock per session (sessionLocks), held
//     across the append, the Sync and the update of received, and taken by finalize, cancel and
//     the expiry too, which read the session again once they hold it. The lock is enough because
//     one process owns the data directory (Boot).
//   - The staging file is the truth, and received is what the client was told: a chunk appends,
//     syncs, then records received with the hashes' state in one synchronous transaction. After a
//     crash the file may be longer than received, and the next request cuts it back.
//   - Every terminal refusal (finalError) makes the session final, and is kept with the core
//     error's code; a success keeps the object as the reply showed it. Every repeat of the
//     finalize, and every query, answers with what was kept.

// The principal kinds a session row keeps.
const (
	kindAnonymous = "anonymous"
	kindUser      = "user"
	kindAdmin     = "admin"
)

// sessionPrincipal is a session's bound identity in its principal column: claims, never a token.
type sessionPrincipal struct {
	UID            string         `json:"uid,omitempty"`
	Token          map[string]any `json:"token,omitempty"`
	AuthTime       time.Time      `json:"authTime,omitzero"`
	IssuedAt       time.Time      `json:"issuedAt,omitzero"`
	ServiceAccount string         `json:"serviceAccount,omitempty"`
	ConsoleAccount string         `json:"consoleAccount,omitempty"`
}

func encodePrincipal(p Principal) (string, json.RawMessage, error) {
	var kind string
	switch p.Kind {
	case PrincipalAnonymous:
		kind = kindAnonymous
	case PrincipalUser:
		if p.UID == "" {
			return "", nil, errors.New("storage: a user principal without a uid")
		}
		kind = kindUser
	case PrincipalAdmin:
		kind = kindAdmin
	default:
		return "", nil, fmt.Errorf("storage: unknown principal kind %d", p.Kind)
	}
	raw, err := json.Marshal(sessionPrincipal{UID: p.UID, Token: p.Token, AuthTime: p.AuthTime, IssuedAt: p.IssuedAt,
		ServiceAccount: p.ServiceAccount, ConsoleAccount: p.ConsoleAccount})
	return kind, raw, err
}

func decodePrincipal(kind string, raw json.RawMessage) (Principal, error) {
	var v sessionPrincipal
	if err := json.Unmarshal(raw, &v); err != nil {
		return Principal{}, fmt.Errorf("storage: an upload session's principal: %w", err)
	}
	p := Principal{UID: v.UID, Token: v.Token, AuthTime: v.AuthTime, IssuedAt: v.IssuedAt,
		ServiceAccount: v.ServiceAccount, ConsoleAccount: v.ConsoleAccount}
	switch kind {
	case kindAnonymous:
		p.Kind = PrincipalAnonymous
	case kindUser:
		p.Kind = PrincipalUser
	case kindAdmin:
		p.Kind = PrincipalAdmin
	default:
		return Principal{}, fmt.Errorf("storage: an upload session's principal kind %q", kind)
	}
	return p, nil
}

// sessionChecksums and sessionPreconditions are a GCS session's claims and conditions, kept from
// its start to its finalize.
type sessionChecksums struct {
	CRC32C *uint32 `json:"crc32c,omitempty"`
	MD5    []byte  `json:"md5,omitempty"`
}

type sessionPreconditions struct {
	IfGenerationMatch        *int64 `json:"ifGenerationMatch,omitempty"`
	IfGenerationNotMatch     *int64 `json:"ifGenerationNotMatch,omitempty"`
	IfMetagenerationMatch    *int64 `json:"ifMetagenerationMatch,omitempty"`
	IfMetagenerationNotMatch *int64 `json:"ifMetagenerationNotMatch,omitempty"`
}

func encodeChecksums(c Checksums) (json.RawMessage, error) {
	v := sessionChecksums{CRC32C: c.CRC32C}
	if c.MD5 != nil {
		v.MD5 = c.MD5[:]
	}
	return json.Marshal(v)
}

func decodeChecksums(raw json.RawMessage) (Checksums, error) {
	var v sessionChecksums
	if err := json.Unmarshal(raw, &v); err != nil || (v.MD5 != nil && len(v.MD5) != 16) {
		return Checksums{}, fmt.Errorf("storage: an upload session's checksums: %v", err)
	}
	c := Checksums{CRC32C: v.CRC32C}
	if v.MD5 != nil {
		var sum [16]byte
		copy(sum[:], v.MD5)
		c.MD5 = &sum
	}
	return c, nil
}

func encodePreconditions(p Preconditions) (json.RawMessage, error) {
	return json.Marshal(sessionPreconditions(p))
}

func decodePreconditions(raw json.RawMessage) (Preconditions, error) {
	var v sessionPreconditions
	if err := json.Unmarshal(raw, &v); err != nil {
		return Preconditions{}, fmt.Errorf("storage: an upload session's preconditions: %w", err)
	}
	return Preconditions(v), nil
}

func parseSurface(s string) Surface {
	switch s {
	case "firebase":
		return SurfaceFirebase
	case "gcs":
		return SurfaceGCS
	case "console":
		return SurfaceConsole
	}
	return 0
}

// sessionTTL is a session's life from its start once it holds a byte: an hour for the
// dashboard's, upload_session_ttl for the rest.
func (s *Service) sessionTTL(surface string) time.Duration {
	if surface == "console" {
		return s.limits.ConsoleSessionTTL
	}
	return s.limits.UploadSessionTTL
}

// limitAddr is the address the sessions-per-IP limit counts: an IPv6 client's /64, an IPv4
// client's address; nil for the zero Addr.
func limitAddr(ip netip.Addr) *netip.Addr {
	if !ip.IsValid() {
		return nil
	}
	ip = ip.Unmap()
	if ip.Is6() {
		p, _ := ip.Prefix(64)
		ip = p.Addr()
	}
	return &ip
}

// StartUpload starts a resumable upload session bound to the principal, the bucket and the name
// (ADR-022 §6, §7), and returns its upload id. It reserves nothing: it only checks that a
// declared size fits what is left of the quota. The session expires at start + empty_session_ttl
// until its first durable byte moves expires_at to start + upload_session_ttl
// (console_session_ttl for the dashboard's). The live sessions per address count per project.
func (s *Service) StartUpload(ctx context.Context, b Bucket, p Principal, spec UploadSpec) (id string, err error) {
	if err := checkPre(spec.Surface, spec.Pre); err != nil {
		return "", err
	}
	leave, err := s.enter()
	if err != nil {
		return "", err
	}
	defer leave()
	if err := validateName(spec.Name); err != nil {
		return "", err
	}
	switch {
	case spec.Surface != SurfaceFirebase && spec.Surface != SurfaceGCS && spec.Surface != SurfaceConsole:
		return "", ErrInvalidArgument
	case spec.Declared > s.limits.ObjectBytes:
		return "", ErrTooLarge
	case spec.Surface == SurfaceConsole && (p.Kind != PrincipalAdmin || p.ConsoleAccount == ""):
		return "", ErrPermissionDenied
	case spec.Surface == SurfaceConsole && spec.Declared < 0:
		// A dashboard session binds its size when it is issued (ADR-022 §15).
		return "", &InvalidError{Reason: "required", Message: "A dashboard upload declares its size."}
	}
	if _, err := s.incomingObject(b, spec.Name, spec.Attrs, spec.Surface); err != nil {
		return "", err
	}
	attrs, err := encodeAttrs(spec.Attrs)
	if err != nil {
		return "", err
	}
	kind, principal, err := encodePrincipal(p)
	if err != nil {
		return "", err
	}
	checksums, err := encodeChecksums(spec.Want)
	if err != nil {
		return "", err
	}
	pre, err := encodePreconditions(spec.Pre)
	if err != nil {
		return "", err
	}
	if err := s.fitsQuota(ctx, b.Project, spec.Declared, kind == kindAnonymous); err != nil {
		return "", err
	}
	ip := limitAddr(spec.IP)
	id = crypto.RandomString(16)
	idHash := s.keys.HashToken(id)
	var tokens []byte
	if spec.Surface == SurfaceGCS && spec.Attrs.Tokens != nil {
		if tokens, err = s.sealSessionTokens(b.Project, spec.Name, idHash, *spec.Attrs.Tokens); err != nil {
			return "", err
		}
	}
	var declared *int64
	if spec.Declared >= 0 {
		declared = &spec.Declared
	}
	// The staging file first: a crash before the row leaves a file Boot's sweep deletes, never a
	// row without its file.
	staging := blob.StagingKey(b.Project)
	f, err := s.store.CreateStaging(staging)
	if err != nil {
		return "", fmt.Errorf("%w: create %s: %w", ErrUnavailable, staging, err)
	}
	if err := f.Close(); err != nil {
		_ = s.store.Delete(staging)
		return "", fmt.Errorf("%w: create %s: %w", ErrUnavailable, staging, err)
	}
	hook("start.staging")
	now := s.now()
	err = s.inTx(ctx, func(q *queries.Queries) error {
		if err := s.checkSessionLimits(ctx, q, b.Project, idHash, p, ip, true); err != nil {
			return err
		}
		return q.InsertUploadSession(ctx, queries.InsertUploadSessionParams{
			IDHash: idHash, ProjectNumber: b.Project, Name: spec.Name, Surface: spec.Surface.String(),
			PrincipalKind: kind, Principal: principal, ClientIp: ip, DeclaredSize: declared, Attrs: attrs,
			DownloadTokens: tokens, Preconditions: pre, Checksums: checksums, StagingKey: staging,
			Now: now, ExpiresAt: now.Add(s.limits.EmptySessionTTL),
		})
	})
	if err != nil {
		_ = s.store.Delete(staging)
		if errors.Is(err, ErrTooManyUploads) {
			return "", err
		}
		return "", fmt.Errorf("%w: start the session: %w", ErrUnavailable, err)
	}
	return id, nil
}

// checkSessionLimits checks the session caps in the caller's transaction, under the project's
// advisory lock, so two starts or two first bytes never pass one count together: the sessions
// that hold a byte in the project, of the uid and from the address (per project), not counting
// the session itself. At a start (atStart) every session of the uid and of the address counts,
// empty ones too, so one client cannot make rows and staging files without bound; at a
// session's first durable byte the live ones count. Over a cap is ErrTooManyUploads.
func (s *Service) checkSessionLimits(ctx context.Context, q *queries.Queries, project int64, self []byte, p Principal,
	ip *netip.Addr, atStart bool) error {
	var uid *string
	if p.Kind == PrincipalUser {
		uid = &p.UID
	}
	if err := q.LockUploadCaps(ctx, project); err != nil {
		return fmt.Errorf("%w: lock the session caps: %w", ErrUnavailable, err)
	}
	c, err := q.CountUploads(ctx, queries.CountUploadsParams{Uid: uid, ClientIp: ip, ProjectNumber: project, Exclude: self})
	if err != nil {
		return fmt.Errorf("%w: count the sessions: %w", ErrUnavailable, err)
	}
	byUser, byIP := c.ByUserLive, c.ByIpLive
	if atStart {
		byUser, byIP = c.ByUser, c.ByIp
	}
	if c.Project >= int64(s.limits.UploadSessionsPerProject) ||
		(uid != nil && byUser >= int64(s.limits.UploadSessionsPerUser)) ||
		(ip != nil && byIP >= int64(s.limits.UploadSessionsPerIP)) {
		s.metrics.Rejections.WithLabelValues("too_many_uploads").Inc()
		return ErrTooManyUploads
	}
	return nil
}

// errTooManyLive is a session's first durable byte over the caps: terminal, as 402, so the
// client stops instead of retrying a session that cannot become live.
var errTooManyLive = fmt.Errorf("%w: %w", ErrQuotaExceeded, ErrTooManyUploads)

// WriteUpload applies one request to a session: append the chunk (an overlap is ignored, a gap
// is an OffsetError), and finalize when asked. p is the request's principal, nil when it has
// none; the session's own identity, bound at start, is the one that counts. Only the terminal
// errors (finalError) make the session final; their state, or the committed object's snapshot,
// answers every repeat.
func (s *Service) WriteUpload(ctx context.Context, b Bucket, name, id string, p *Principal, c Chunk) (UploadState, error) {
	leave, err := s.enter()
	if err != nil {
		return UploadState{}, err
	}
	defer leave()
	idHash := s.keys.HashToken(id)
	unlock, err := s.sessions.lock(ctx, idHash)
	if err != nil {
		return UploadState{}, err
	}
	defer unlock()
	sess, err := s.lockedSession(ctx, b, name, idHash)
	if err != nil {
		return UploadState{}, err
	}
	if sess.State != "active" {
		return s.replay(b, sess)
	}
	active := UploadState{Status: UploadActive, Received: sess.Received}
	limit, bySize := s.limits.ObjectBytes-sess.Received, false
	if sess.DeclaredSize != nil && *sess.DeclaredSize-sess.Received < limit {
		limit, bySize = *sess.DeclaredSize-sess.Received, true
	}
	switch {
	case c.Offset < 0:
		return active, invalid("The upload offset is negative.")
	case c.Total >= 0 && sess.DeclaredSize != nil && c.Total != *sess.DeclaredSize,
		c.Total >= 0 && c.Total < sess.Received:
		return s.failSession(ctx, sess, ErrSizeMismatch)
	case c.Total > s.limits.ObjectBytes:
		return s.failSession(ctx, sess, ErrTooLarge)
	case c.Offset > sess.Received:
		return active, &OffsetError{Received: sess.Received}
	}
	if c.Total >= 0 && c.Total-sess.Received < limit {
		limit, bySize = c.Total-sess.Received, true
	}
	body := c.Body
	if body == nil {
		body = eofReader{}
	}
	// The bytes before received are stored already and are not written again (GCS: "Cloud
	// Storage ignores attempts to do so").
	length := c.Length
	if skip := sess.Received - c.Offset; skip > 0 {
		n, err := io.CopyN(io.Discard, body, skip)
		switch {
		case n < skip && (err == nil || errors.Is(err, io.EOF)):
			length = 0
		case err != nil:
			return active, err
		case length >= 0:
			length -= skip
		}
	}
	if length != 0 {
		if sess.BlobKey != nil {
			// A finalize already fixed the object's size and moved its file: a byte past it
			// does not match.
			if k, _ := io.ReadFull(body, make([]byte, 1)); k > 0 || length > 0 {
				return s.failSession(ctx, sess, ErrSizeMismatch)
			}
		} else if err := s.appendChunk(ctx, &sess, body, length, limit); err != nil {
			if errors.Is(err, errOverLimit) {
				err = ErrTooLarge
				if bySize {
					err = ErrSizeMismatch
				}
			}
			return s.failSession(ctx, sess, err)
		}
	}
	if !c.Finalize {
		return UploadState{Status: UploadActive, Received: sess.Received}, nil
	}
	return s.finalize(ctx, b, sess, c)
}

// eofReader is an empty body.
type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

// lockedSession reads a session under its lock: an id of another project or name is
// ErrUploadNotFound, and a session whose time is up expires now and is ErrUploadNotFound too.
func (s *Service) lockedSession(ctx context.Context, b Bucket, name string, idHash []byte) (queries.StorageUploadSession, error) {
	sess, err := s.readSession(ctx, b, name, idHash)
	if err != nil {
		return sess, err
	}
	if !s.now().Before(sess.ExpiresAt) {
		if err := s.expireSession(ctx, sess.ProjectNumber, idHash); err != nil {
			return sess, err
		}
		return sess, ErrUploadNotFound
	}
	return sess, nil
}

// readSession reads a session of the bucket's project with the name it was started for.
func (s *Service) readSession(ctx context.Context, b Bucket, name string, idHash []byte) (queries.StorageUploadSession, error) {
	sess, err := s.reads.GetUploadSession(ctx, queries.GetUploadSessionParams{IDHash: idHash, ProjectNumber: b.Project})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return sess, ErrUploadNotFound
	case err != nil:
		return sess, fmt.Errorf("%w: read the session: %w", ErrUnavailable, err)
	case sess.Name != name:
		return sess, ErrUploadNotFound
	}
	return sess, nil
}

// appendChunk appends up to limit bytes of body (length of them when known) to the session's
// staging file, syncs it, then records the new received offset, the hashes' state and the
// session's reservation in one transaction. Whatever arrived before an error is kept, synced
// and announced, unless the store wrote other than it read. sess is updated to what was
// recorded.
func (s *Service) appendChunk(ctx context.Context, sess *queries.StorageUploadSession, body io.Reader, length, limit int64) error {
	st, err := s.store.OpenStaging(sess.StagingKey)
	if err != nil {
		return fmt.Errorf("%w: open %s: %w", ErrUnavailable, sess.StagingKey, err)
	}
	defer st.Close()
	if err := s.cutStaging(st, sess); err != nil {
		return err
	}
	h, err := restoreHasher(sess.HashState)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	held := max(sess.ReservedBytes-sess.Received, 0)
	res := &reservation{project: sess.ProjectNumber, anonymous: sess.PrincipalKind == kindAnonymous,
		session: sess.IDHash, n: held}
	n, serr := s.stream(ctx, st.Append, body, h, length, limit, res)
	hook("upload.appended")
	if errors.Is(serr, errStoreMismatch) {
		// Nothing of this request is announced; the next one cuts the file back to received.
		return fmt.Errorf("%w: %w", ErrUnavailable, serr)
	}
	if n == 0 && res.n == held {
		return serr
	}
	if n > 0 {
		if err := st.Sync(); err != nil {
			return fmt.Errorf("%w: sync %s: %w", ErrUnavailable, sess.StagingKey, err)
		}
	}
	hook("upload.synced")
	state, err := h.marshal()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	received := sess.Received + n
	progressed, expires := sess.ProgressedAt, sess.ExpiresAt
	if n > 0 {
		progressed = s.now()
		if sess.Received == 0 {
			// The first durable byte: the session lives its full life from its start.
			expires = sess.CreatedAt.Add(s.sessionTTL(sess.Surface))
		}
	}
	// The request gives back what it reserved and did not write: at rest a session holds its
	// durable bytes.
	release := sess.Received + res.n - received
	// What arrived is recorded even when the request is gone: net/http cancels its context the
	// moment the body breaks, and the client resumes from what this announces. Every statement
	// of the transaction runs on the uncancelled context, the session caps' included.
	ctx = context.WithoutCancel(ctx)
	err = s.inTx(ctx, func(q *queries.Queries) error {
		if sess.Received == 0 && received > 0 {
			// The session becomes live: the caps count it from now on.
			p, err := decodePrincipal(sess.PrincipalKind, sess.Principal)
			if err != nil {
				return err
			}
			if err := s.checkSessionLimits(ctx, q, sess.ProjectNumber, sess.IDHash, p, sess.ClientIp, false); err != nil {
				return err
			}
		}
		rows, err := q.RecordUploadProgress(ctx, queries.RecordUploadProgressParams{
			Received: received, HashState: state, ReservedBytes: received, Now: progressed, ExpiresAt: expires,
			IDHash: sess.IDHash, ProjectNumber: sess.ProjectNumber,
		})
		if err != nil {
			return err
		}
		if rows == 0 {
			return errors.New("the session is no longer active")
		}
		return unreserve(ctx, q, sess.ProjectNumber, release, res.anonymous)
	})
	if errors.Is(err, ErrTooManyUploads) {
		return errTooManyLive
	}
	if err != nil {
		return fmt.Errorf("%w: record the upload's progress: %w", ErrUnavailable, err)
	}
	sess.Received, sess.HashState, sess.ReservedBytes = received, state, received
	sess.ProgressedAt, sess.ExpiresAt = progressed, expires
	return serr
}

// cutStaging cuts a staging file back to the received bytes, what a crash between its append and
// the update of received left beyond them. A file shorter than received lost synced bytes.
func (s *Service) cutStaging(st blob.Staging, sess *queries.StorageUploadSession) error {
	size, err := st.Size()
	if err != nil {
		return fmt.Errorf("%w: size of %s: %w", ErrUnavailable, sess.StagingKey, err)
	}
	switch {
	case size > sess.Received:
		if err := st.Truncate(sess.Received); err != nil {
			return fmt.Errorf("%w: truncate %s: %w", ErrUnavailable, sess.StagingKey, err)
		}
		return st.Sync()
	case size < sess.Received:
		s.log.Error("storage: a staging file is shorter than the bytes it announced", "key", sess.StagingKey,
			"size", size, "received", sess.Received)
		return fmt.Errorf("%w: the staging file lost bytes", ErrUnavailable)
	}
	return nil
}

// finalize ends an active session with the bytes it holds: the declared size, the client's
// checksums, the identity's live state, the dashboard account's ownership and the rules, all
// before the staging file moves; then blob_key, the move, and commitObject, which makes the
// session final in its own transaction.
func (s *Service) finalize(ctx context.Context, b Bucket, sess queries.StorageUploadSession, c Chunk) (UploadState, error) {
	active := UploadState{Status: UploadActive, Received: sess.Received}
	if (sess.DeclaredSize != nil && sess.Received != *sess.DeclaredSize) || (c.Total >= 0 && sess.Received != c.Total) {
		return s.failSession(ctx, sess, ErrSizeMismatch)
	}
	want, err := decodeChecksums(sess.Checksums)
	if err != nil {
		return active, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	want, err = mergeChecksums(want, c.Want)
	if err != nil {
		return s.failSession(ctx, sess, err)
	}
	h, err := restoreHasher(sess.HashState)
	if err != nil {
		return active, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	crc, sum := h.sums()
	if err := checkChecksums(want, crc, sum); err != nil {
		return s.failSession(ctx, sess, err)
	}
	p, err := decodePrincipal(sess.PrincipalKind, sess.Principal)
	if err != nil {
		return active, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err := s.checkIdentity(ctx, b, p); err != nil {
		return s.failSession(ctx, sess, err)
	}
	attrs, err := decodeAttrs(sess.Attrs)
	if err != nil {
		return active, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	surface := parseSurface(sess.Surface)
	if surface == SurfaceGCS && sess.DownloadTokens != nil {
		tokens, err := s.openSessionTokens(sess.ProjectNumber, sess.Name, sess.IDHash, sess.DownloadTokens)
		if err != nil {
			return active, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		attrs.Tokens = &tokens
	}
	pre, err := decodePreconditions(sess.Preconditions)
	if err != nil {
		return active, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	o, err := s.incomingObject(b, sess.Name, attrs, surface)
	if err != nil {
		return active, err
	}
	o.Size, o.CRC32C, o.MD5 = sess.Received, crc, sum
	// The session's identity is known only now; no transaction is open yet.
	if ctx, err = s.prepareRules(ctx, b, p); err != nil {
		return active, err
	}
	if err := s.decideCreate(ctx, b, p, &o, pre, surface); err != nil {
		return s.failSession(ctx, sess, err)
	}
	key, err := s.placeBlob(ctx, &sess)
	if err != nil {
		return active, err
	}
	obj, err := s.commitObject(ctx, objectWrite{bucket: b, object: o, blobKey: key, pre: pre,
		reserved: sess.ReservedBytes, session: sess.IDHash})
	if err != nil {
		// Unless terminal, the session keeps blob_key: its next finalize commits the moved file,
		// or finds the commit that happened.
		return s.failSession(ctx, sess, err)
	}
	if p.ConsoleAccount != "" {
		s.auditConsole(ctx, b, p, obj)
	}
	return UploadState{Status: UploadFinal, Received: sess.Received, Object: &obj}, nil
}

// checkIdentity checks that a session's identity still stands at finalize (ADR-022 §7, §15): a
// user deleted, disabled or revoked since the start is ErrUnauthenticated; a dashboard account
// that no longer owns the project is ErrPermissionDenied. An outage is ErrUnavailable and leaves
// the session active.
func (s *Service) checkIdentity(ctx context.Context, b Bucket, p Principal) error {
	switch {
	case p.Kind == PrincipalUser:
		if s.users == nil {
			return fmt.Errorf("%w: no live user state", ErrUnavailable)
		}
		st, err := s.users.State(ctx, b.Project, p.UID)
		if err != nil {
			return fmt.Errorf("%w: the user's state: %w", ErrUnavailable, err)
		}
		if err := st.Admits(p.AuthTime, p.IssuedAt); err != nil {
			return fmt.Errorf("%w: %w", ErrUnauthenticated, err)
		}
	case p.Kind == PrincipalAdmin && p.ConsoleAccount != "":
		account, err := uuid.Parse(p.ConsoleAccount)
		if err != nil {
			return ErrPermissionDenied
		}
		proj, err := s.projects.GetProject(ctx, consolePrincipal(account), strconv.FormatInt(b.Project, 10))
		switch {
		case apierr.CodeOf(err) == apierr.NotFound || apierr.CodeOf(err) == apierr.PermissionDenied:
			return ErrPermissionDenied
		case err != nil:
			return fmt.Errorf("%w: the dashboard account: %w", ErrUnavailable, err)
		case proj.Number != b.Project || proj.State != "ACTIVE":
			return ErrPermissionDenied
		}
	}
	return nil
}

func consolePrincipal(account uuid.UUID) *authn.Principal {
	return &authn.Principal{Kind: authn.KindAccount, Method: authn.MethodSession, AccountID: account}
}

// auditConsole records a dashboard upload once it committed (ADR-022 §15). The object stands
// whatever the log says: a failure is logged.
func (s *Service) auditConsole(ctx context.Context, b Bucket, p Principal, o Object) {
	account, err := uuid.Parse(p.ConsoleAccount)
	if err == nil {
		err = s.projects.Audit(context.WithoutCancel(ctx), consolePrincipal(account), b.Project, "storage.object.upload",
			o.Name, map[string]any{"size": o.Size, "generation": strconv.FormatInt(o.Generation, 10)})
	}
	if err != nil {
		s.log.Error("storage: audit a dashboard upload", "error", err)
	}
}

// placeBlob moves the staging file to the session's object key: blob_key is written first, so a
// finalize after a crash finds the file. A session with blob_key resumes: Promote is called
// again, which makes a file an earlier move left at the key durable, and the file at the key is
// hashed again and must be the session's bytes before anything commits.
func (s *Service) placeBlob(ctx context.Context, sess *queries.StorageUploadSession) (string, error) {
	if sess.BlobKey != nil {
		key := *sess.BlobKey
		if err := s.promote(sess, key, true); err != nil {
			return "", err
		}
		return key, nil
	}
	key := blob.ObjectKey(sess.ProjectNumber)
	err := s.inTx(ctx, func(q *queries.Queries) error {
		rows, err := q.SetUploadBlobKey(ctx, queries.SetUploadBlobKeyParams{BlobKey: &key, IDHash: sess.IDHash,
			ProjectNumber: sess.ProjectNumber})
		if err == nil && rows == 0 {
			err = errors.New("the session is no longer active")
		}
		return err
	})
	if err != nil {
		return "", fmt.Errorf("%w: write blob_key: %w", ErrUnavailable, err)
	}
	sess.BlobKey = &key
	hook("finalize.blob_key")
	if err := s.promote(sess, key, false); err != nil {
		return "", err
	}
	hook("finalize.promoted")
	return key, nil
}

// promote cuts the staging file to received, what a crash after an append may have left beyond
// it, and moves it to key. When the file at the key was not moved by this call (a resume, where
// the staging file may be gone, or Promote saying it was moved earlier), its bytes are checked
// against the session's before the caller may commit.
func (s *Service) promote(sess *queries.StorageUploadSession, key string, resume bool) error {
	st, err := s.store.OpenStaging(sess.StagingKey)
	switch {
	case err == nil:
		err = s.cutStaging(st, sess)
		if cerr := st.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	case !resume:
		return fmt.Errorf("%w: open %s: %w", ErrUnavailable, sess.StagingKey, err)
	}
	err = s.store.Promote(sess.StagingKey, key)
	earlier := errors.Is(err, blob.ErrPromotedEarlier)
	if err != nil && !earlier {
		return fmt.Errorf("%w: move %s: %w", ErrUnavailable, sess.StagingKey, err)
	}
	if resume || earlier {
		return s.verifyPlaced(sess, key)
	}
	return nil
}

// verifyPlaced hashes the file at a session's object key and compares its size, crc32c and md5
// with what the session recorded. A file that differs is never committed: the request fails with
// ErrUnavailable and the session stays active, and the file is left where it is for the
// operator; the session's expiry queues it.
func (s *Service) verifyPlaced(sess *queries.StorageUploadSession, key string) error {
	want, err := restoreHasher(sess.HashState)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	wantCRC, wantMD5 := want.sums()
	r, err := s.store.Open(key)
	if err != nil {
		return fmt.Errorf("%w: open %s: %w", ErrUnavailable, key, err)
	}
	defer r.Close()
	h := newHasher()
	n, err := io.Copy(h, r)
	if err != nil {
		return fmt.Errorf("%w: read %s: %w", ErrUnavailable, key, err)
	}
	crc, sum := h.sums()
	if n != sess.Received || crc != wantCRC || sum != wantMD5 {
		s.log.Error("storage: the file an upload moved to its object key is not the bytes it received; it is not committed",
			"key", key, "size", n, "received", sess.Received)
		return fmt.Errorf("%w: %s does not hold the upload's bytes", ErrUnavailable, key)
	}
	return nil
}

// finishSession makes a session final with the object it committed, in commitObject's
// transaction: the snapshot every repeat answers with, and its tokens sealed for the session.
func (s *Service) finishSession(ctx context.Context, q *queries.Queries, sess queries.StorageUploadSession, o Object) error {
	snapshot, err := encodeSnapshot(o)
	if err != nil {
		return err
	}
	tokens, err := s.sealSessionTokens(sess.ProjectNumber, sess.Name, sess.IDHash, o.DownloadTokens)
	if err != nil {
		return err
	}
	rows, err := q.FinishUploadSession(ctx, queries.FinishUploadSessionParams{ResultObject: snapshot, DownloadTokens: tokens,
		ExpiresAt: sess.CreatedAt.Add(s.sessionTTL(sess.Surface)), IDHash: sess.IDHash, ProjectNumber: sess.ProjectNumber})
	if err != nil {
		return fmt.Errorf("%w: finish the session: %w", ErrUnavailable, err)
	}
	if rows == 0 {
		return fmt.Errorf("%w: the session is no longer active", ErrUnavailable)
	}
	return nil
}

// failSession makes a session final on a terminal refusal, keeping the error's code for every
// repeat, gives its whole reservation back, and queues its files. Any other error leaves it
// active and is returned as it is. When the refusal cannot be recorded, the request fails with
// ErrUnavailable and the session stays active, so its next request refuses again.
func (s *Service) failSession(ctx context.Context, sess queries.StorageUploadSession, cause error) (UploadState, error) {
	e, ok := sessionFinal(cause, sess.Surface)
	if !ok {
		return UploadState{Status: UploadActive, Received: sess.Received}, cause
	}
	code, msg := e.code, e.msg
	ctx = context.WithoutCancel(ctx)
	err := s.inTx(ctx, func(q *queries.Queries) error {
		cur, err := q.GetUploadSession(ctx, queries.GetUploadSessionParams{IDHash: sess.IDHash, ProjectNumber: sess.ProjectNumber})
		if err != nil {
			return err
		}
		rows, err := q.FailUploadSession(ctx, queries.FailUploadSessionParams{FinalStatus: &code, FinalBody: &msg,
			ExpiresAt: cur.CreatedAt.Add(s.sessionTTL(cur.Surface)), IDHash: cur.IDHash, ProjectNumber: cur.ProjectNumber})
		if err != nil {
			return err
		}
		if rows == 0 {
			return errors.New("the session is no longer active")
		}
		return s.dropSessionFiles(ctx, q, cur)
	})
	if err != nil {
		return UploadState{Status: UploadActive, Received: sess.Received}, fmt.Errorf("%w: record the refusal: %w", ErrUnavailable, err)
	}
	return UploadState{Status: UploadFinal, Received: sess.Received, Err: e}, e
}

// sessionFinal is finalError for a session of surface: on GCS a precondition that fails at
// finalize is terminal too (ADR-023 §3-55), so its staged bytes and reservation go at once.
// /v0 and the dashboard never carry preconditions (checkPre).
func sessionFinal(err error, surface string) (*Error, bool) {
	if e, ok := finalError(err); ok {
		return e, true
	}
	if surface == "gcs" && errors.Is(err, ErrPreconditionFailed) {
		return ErrPreconditionFailed, true
	}
	return nil, false
}

// decodeSessionError is decodeError for a final session of surface, with sessionFinal's GCS
// precondition.
func decodeSessionError(code, surface string) error {
	if surface == "gcs" && code == ErrPreconditionFailed.code {
		return ErrPreconditionFailed
	}
	return decodeError(code)
}

// dropSessionFiles gives back what a session that ends without an object holds, in the caller's
// transaction: its reservation, and its files to blob_gc (the staging file, and the object key
// it moved to but never committed).
func (s *Service) dropSessionFiles(ctx context.Context, q *queries.Queries, sess queries.StorageUploadSession) error {
	if sess.State == "active" {
		if err := unreserve(ctx, q, sess.ProjectNumber, sess.ReservedBytes, sess.PrincipalKind == kindAnonymous); err != nil {
			return err
		}
	}
	if err := q.QueueUnreferencedBlob(ctx, queries.QueueUnreferencedBlobParams{BlobKey: sess.StagingKey, ProjectNumber: sess.ProjectNumber}); err != nil {
		return err
	}
	if sess.BlobKey != nil {
		return q.QueueUnreferencedBlob(ctx, queries.QueueUnreferencedBlobParams{BlobKey: *sess.BlobKey, ProjectNumber: sess.ProjectNumber})
	}
	return nil
}

// replay answers a request on a session that is no longer active with what it kept: the object
// it committed, the refusal it ended with, or its cancellation.
func (s *Service) replay(b Bucket, sess queries.StorageUploadSession) (UploadState, error) {
	switch {
	case sess.State == "cancelled":
		return UploadState{Status: UploadCancelled, Received: sess.Received}, ErrUploadCancelled
	case sess.ResultObject != nil:
		o, err := decodeSnapshot(sess.ResultObject)
		if err != nil {
			return UploadState{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if o.DownloadTokens, err = s.openSessionTokens(sess.ProjectNumber, sess.Name, sess.IDHash, sess.DownloadTokens); err != nil {
			return UploadState{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		o.Bucket = b.Name
		return UploadState{Status: UploadFinal, Received: sess.Received, Object: &o}, nil
	}
	e := decodeSessionError(deref(sess.FinalStatus), sess.Surface)
	return UploadState{Status: UploadFinal, Received: sess.Received, Err: e}, e
}

// QueryUpload returns a session's state. It takes no lock: received is only ever written once
// durable, so what it reads may be announced.
func (s *Service) QueryUpload(ctx context.Context, b Bucket, name, id string) (UploadState, error) {
	sess, err := s.readSession(ctx, b, name, s.keys.HashToken(id))
	if err != nil {
		return UploadState{}, err
	}
	if !s.now().Before(sess.ExpiresAt) {
		return UploadState{}, ErrUploadNotFound
	}
	if sess.State != "active" {
		return s.replay(b, sess)
	}
	return UploadState{Status: UploadActive, Received: sess.Received}, nil
}

// CancelUpload cancels a session and frees its staging file and reservation; cancelling twice
// is not an error, cancelling a final session is ErrUploadFinal.
func (s *Service) CancelUpload(ctx context.Context, b Bucket, name, id string) (UploadState, error) {
	leave, err := s.enter()
	if err != nil {
		return UploadState{}, err
	}
	defer leave()
	idHash := s.keys.HashToken(id)
	unlock, err := s.sessions.lock(ctx, idHash)
	if err != nil {
		return UploadState{}, err
	}
	defer unlock()
	sess, err := s.lockedSession(ctx, b, name, idHash)
	if err != nil {
		return UploadState{}, err
	}
	switch sess.State {
	case "cancelled":
		return UploadState{Status: UploadCancelled, Received: sess.Received}, nil
	case "final":
		st, _ := s.replay(b, sess)
		return st, ErrUploadFinal
	}
	ctx = context.WithoutCancel(ctx)
	err = s.inTx(ctx, func(q *queries.Queries) error {
		rows, err := q.CancelUploadSession(ctx, queries.CancelUploadSessionParams{IDHash: idHash, ProjectNumber: b.Project})
		if err != nil {
			return err
		}
		if rows == 0 {
			return errors.New("the session is no longer active")
		}
		return s.dropSessionFiles(ctx, q, sess)
	})
	if err != nil {
		return UploadState{Status: UploadActive, Received: sess.Received}, fmt.Errorf("%w: cancel: %w", ErrUnavailable, err)
	}
	return UploadState{Status: UploadCancelled, Received: sess.Received}, nil
}

// lock takes a session's lock and returns its release. A request whose context ends while it
// waits leaves without it: ErrUnavailable, which leaves the session as it was, and its client,
// gone, hears nothing.
func (l *sessionLocks) lock(ctx context.Context, idHash []byte) (func(), error) {
	k := string(idHash)
	e := l.acquire(k)
	select {
	case e.held <- struct{}{}:
	case <-ctx.Done():
		l.drop(k, e)
		return nil, fmt.Errorf("%w: waiting for the upload session: %w", ErrUnavailable, context.Cause(ctx))
	}
	return func() {
		<-e.held
		l.drop(k, e)
	}, nil
}

// tryLock takes a session's lock when it is free: the background work passes over a session a
// request is working on, and finds it on a later round.
func (l *sessionLocks) tryLock(idHash []byte) (func(), bool) {
	k := string(idHash)
	e := l.acquire(k)
	select {
	case e.held <- struct{}{}:
	default:
		l.drop(k, e)
		return nil, false
	}
	return func() {
		<-e.held
		l.drop(k, e)
	}, true
}

func (l *sessionLocks) acquire(k string) *sessionLock {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held == nil {
		l.held = map[string]*sessionLock{}
	}
	e := l.held[k]
	if e == nil {
		e = &sessionLock{held: make(chan struct{}, 1)}
		l.held[k] = e
	}
	e.refs++
	return e
}

func (l *sessionLocks) drop(k string, e *sessionLock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e.refs--; e.refs == 0 {
		delete(l.held, k)
	}
}
