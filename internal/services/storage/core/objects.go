package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// maxRaces bounds how many times an operation reads an object again because another request
// changed it between the read the operation decided on and its write. Past it the operation is
// ErrUnavailable, which the client retries (503; ADR-023 §3-45).
const maxRaces = 8

// Stat returns an object's metadata after the rules allow a get. On SurfaceFirebase an object
// without a download token gets one, atomically with a new metageneration (ADR-022 §9). gen is
// a generation the caller pins (GCS ?generation=); 0 is the current one, and any other than the
// current is ErrNotFound.
//
// The rules decide on the object as it is read, and the token is written only if the row is
// still that one: readers that race on an object without a token read one metageneration, one
// of them writes, and the others read again and find its token.
func (s *Service) Stat(ctx context.Context, b Bucket, name string, p Principal, sf Surface, gen int64) (Object, error) {
	if err := validateName(name); err != nil {
		return Object{}, err
	}
	for range maxRaces {
		cur, err := s.current(ctx, b, name)
		if err != nil {
			return Object{}, err
		}
		if err := s.authorize(ctx, b, p, ruleOp{Method: rules.MethodGet, Name: name, Resource: cur, Surface: sf}); err != nil {
			return Object{}, err
		}
		if cur == nil || (gen != 0 && gen != cur.Generation) {
			return Object{}, ErrNotFound
		}
		if sf != SurfaceFirebase || len(cur.DownloadTokens) > 0 {
			return *cur, nil
		}
		next := *cur
		next.DownloadTokens = []string{newDownloadToken()}
		o, err := s.write(ctx, b, *cur, next)
		if errors.Is(err, errRaced) {
			continue
		}
		return o, err
	}
	return Object{}, raced(name)
}

// Open opens an object's bytes. A token that matches one of the object's opens it without the
// rules; a token that is present and matches none is ErrPermissionDenied without the rules
// (ADR-022 §9); no token, and the rules decide a get. The caller closes the reader.
//
// A token is compared after the object is found by its name, in constant time, against the
// tokens opened from the row's seal; nothing is minted here.
func (s *Service) Open(ctx context.Context, b Bucket, name string, p Principal, token string, gen int64) (Object, blob.Reader, error) {
	if err := validateName(name); err != nil {
		return Object{}, nil, err
	}
	for range maxRaces {
		row, err := s.reads.GetObject(ctx, queries.GetObjectParams{ProjectNumber: b.Project, Name: name})
		found := err == nil
		if !found && !errors.Is(err, pgx.ErrNoRows) {
			return Object{}, nil, fmt.Errorf("%w: read %q: %w", ErrUnavailable, name, err)
		}
		var cur *Object
		if found {
			o, err := s.objectFromRow(b, row)
			if err != nil {
				if token != "" {
					// A seal that does not open (a row copied from elsewhere) matches no token.
					return Object{}, nil, ErrPermissionDenied
				}
				return Object{}, nil, err
			}
			cur = &o
		}
		if token != "" {
			if cur == nil || !matchToken(cur.DownloadTokens, token) {
				return Object{}, nil, ErrPermissionDenied
			}
		} else {
			// Only /v0 opens bytes for principals the rules decide on: the GCS surface and the
			// dashboard act as admins.
			if err := s.authorize(ctx, b, p, ruleOp{Method: rules.MethodGet, Name: name, Resource: cur, Surface: SurfaceFirebase}); err != nil {
				return Object{}, nil, err
			}
			if cur == nil {
				return Object{}, nil, ErrNotFound
			}
		}
		if gen != 0 && gen != cur.Generation {
			return Object{}, nil, ErrNotFound
		}
		r, err := s.store.Open(row.BlobKey)
		if err != nil {
			// The row may have been replaced or deleted since it was read, and its file
			// collected after gc_grace: read it again. The same row without its file is the
			// server's fault.
			again, rerr := s.reads.GetObject(ctx, queries.GetObjectParams{ProjectNumber: b.Project, Name: name})
			if errors.Is(rerr, pgx.ErrNoRows) || (rerr == nil && again.BlobKey != row.BlobKey) {
				continue
			}
			s.log.Error("storage: an object's file does not open", "project", b.Project, "key", row.BlobKey, "err", err)
			return Object{}, nil, fmt.Errorf("%w: open %s: %w", ErrUnavailable, row.BlobKey, err)
		}
		if r.Size() != row.Size {
			_ = r.Close()
			s.log.Error("storage: an object's file is not its size", "project", b.Project, "key", row.BlobKey,
				"size", r.Size(), "want", row.Size)
			return Object{}, nil, fmt.Errorf("%w: %s is %d bytes, not %d", ErrUnavailable, row.BlobKey, r.Size(), row.Size)
		}
		return *cur, r, nil
	}
	return Object{}, nil, raced(name)
}

// Update applies a metadata change after the rules allow an update, and advances the
// metageneration (ADR-022 §8, §14).
//
// The change is checked before anything is read (checkPatch), and its result after the rules,
// which see the object as it is and as the change leaves it (request.resource), with the same
// generation and metageneration.
func (s *Service) Update(ctx context.Context, b Bucket, name string, p Principal, sf Surface, u Patch, pre Preconditions) (Object, error) {
	if err := checkPre(sf, pre); err != nil {
		return Object{}, err
	}
	if err := validateName(name); err != nil {
		return Object{}, err
	}
	u, err := surfacePatch(u, sf)
	if err != nil {
		return Object{}, err
	}
	if err := s.checkPatch(u); err != nil {
		return Object{}, err
	}
	return s.change(ctx, b, name, func(cur *Object) (Object, error) {
		var next *Object
		if cur != nil {
			n := applyPatch(*cur, u)
			next = &n
		}
		op := ruleOp{Method: rules.MethodUpdate, Name: name, Resource: cur, Incoming: next, Patch: &u, Surface: sf}
		if err := s.authorize(ctx, b, p, op); err != nil {
			return Object{}, err
		}
		if cur == nil {
			return Object{}, ErrNotFound
		}
		if err := checkPreconditions(*cur, pre); err != nil {
			return Object{}, err
		}
		if err := s.checkObjectMetadata(*next); err != nil {
			return Object{}, err
		}
		return *next, nil
	})
}

// Delete removes an object after the rules allow a delete; its file goes to blob_gc in the same
// transaction (releaseObject). Preconditions come from the GCS surface alone.
func (s *Service) Delete(ctx context.Context, b Bucket, name string, p Principal, sf Surface, pre Preconditions) error {
	if err := checkPre(sf, pre); err != nil {
		return err
	}
	if err := validateName(name); err != nil {
		return err
	}
	for range maxRaces {
		cur, err := s.current(ctx, b, name)
		if err != nil {
			return err
		}
		if err := s.authorize(ctx, b, p, ruleOp{Method: rules.MethodDelete, Name: name, Resource: cur, Surface: sf}); err != nil {
			return err
		}
		if cur == nil {
			return ErrNotFound
		}
		if err := checkPreconditions(*cur, pre); err != nil {
			return err
		}
		err = s.inTx(ctx, func(q *queries.Queries) error {
			gone, err := q.DeleteObject(ctx, queries.DeleteObjectParams{
				ProjectNumber: b.Project, Name: name, Generation: cur.Generation, Metageneration: cur.Metageneration,
			})
			if err != nil {
				return err
			}
			return releaseObject(ctx, q, b.Project, gone.BlobKey, gone.Size)
		})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			continue
		case errors.Is(err, ErrUnavailable):
			return err
		case err != nil:
			return fmt.Errorf("%w: delete %q: %w", ErrUnavailable, name, err)
		}
		return nil
	}
	return raced(name)
}

// checkPreconditions checks GCS's preconditions on the object they apply to (B §10.2-8):
// ifGenerationMatch = 0 requires that no object exists, so it fails on any.
func checkPreconditions(o Object, pre Preconditions) error {
	switch {
	case pre.IfGenerationMatch != nil && *pre.IfGenerationMatch != o.Generation,
		pre.IfGenerationNotMatch != nil && *pre.IfGenerationNotMatch == o.Generation,
		pre.IfMetagenerationMatch != nil && *pre.IfMetagenerationMatch != o.Metageneration,
		pre.IfMetagenerationNotMatch != nil && *pre.IfMetagenerationNotMatch == o.Metageneration:
		return ErrPreconditionFailed
	}
	return nil
}

// errRaced is a write whose row changed after it was read: the caller reads it again.
var errRaced = errors.New("storage: the object changed in between")

// raced is the error of an operation that lost maxRaces races in a row.
func raced(name string) error {
	return fmt.Errorf("%w: %q changed %d times during one request", ErrUnavailable, name, maxRaces)
}

// current reads an object's row as an Object, nil when there is none.
func (s *Service) current(ctx context.Context, b Bucket, name string) (*Object, error) {
	row, err := s.reads.GetObject(ctx, queries.GetObjectParams{ProjectNumber: b.Project, Name: name})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read %q: %w", ErrUnavailable, name, err)
	}
	o, err := s.objectFromRow(b, row)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// change runs a change of an object's metadata or tokens: decide receives the object as it is
// (nil when there is none) and returns what to store, or the refusal; the result is written if
// the row is still the one decide saw, else decided again.
func (s *Service) change(ctx context.Context, b Bucket, name string, decide func(cur *Object) (Object, error)) (Object, error) {
	for range maxRaces {
		cur, err := s.current(ctx, b, name)
		if err != nil {
			return Object{}, err
		}
		next, err := decide(cur)
		if err != nil {
			return Object{}, err
		}
		if cur == nil {
			return Object{}, ErrNotFound
		}
		o, err := s.write(ctx, b, *cur, next)
		if errors.Is(err, errRaced) {
			continue
		}
		return o, err
	}
	return Object{}, raced(name)
}

// write stores next's content fields, custom metadata and tokens over cur's row, with a new
// metageneration, if the row still has cur's generation and metageneration; errRaced if not.
func (s *Service) write(ctx context.Context, b Bucket, cur, next Object) (Object, error) {
	sealed, err := s.sealTokens(b.Project, cur.Name, next.DownloadTokens)
	if err != nil {
		return Object{}, err
	}
	md, err := encodeMetadata(next.Metadata)
	if err != nil {
		return Object{}, fmt.Errorf("storage: encode the metadata of %q: %w", cur.Name, err)
	}
	var row queries.StorageObject
	err = s.inTx(ctx, func(q *queries.Queries) error {
		var err error
		row, err = q.UpdateObject(ctx, queries.UpdateObjectParams{
			ProjectNumber: b.Project, Name: cur.Name, Generation: cur.Generation, Metageneration: cur.Metageneration,
			ContentType: nullable(next.ContentType), ContentEncoding: nullable(next.ContentEncoding),
			ContentDisposition: nullable(next.ContentDisposition), ContentLanguage: nullable(next.ContentLanguage),
			CacheControl: nullable(next.CacheControl), Metadata: md, DownloadTokens: sealed, Now: s.now(),
		})
		return err
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return Object{}, errRaced
	case errors.Is(err, ErrUnavailable):
		return Object{}, err
	case err != nil:
		return Object{}, fmt.Errorf("%w: update %q: %w", ErrUnavailable, cur.Name, err)
	}
	return s.objectFromRow(b, row)
}

// objectFromRow returns an object's row as an Object, its tokens opened from their seal.
func (s *Service) objectFromRow(b Bucket, r queries.StorageObject) (Object, error) {
	o := Object{
		Bucket: b.Name, Name: r.Name, Generation: r.Generation, Metageneration: r.Metageneration, Size: r.Size,
		CRC32C: uint32(r.Crc32c), ContentType: deref(r.ContentType), ContentEncoding: deref(r.ContentEncoding),
		ContentDisposition: deref(r.ContentDisposition), ContentLanguage: deref(r.ContentLanguage),
		CacheControl: deref(r.CacheControl), Created: r.TimeCreated, Updated: r.Updated,
	}
	copy(o.MD5[:], r.Md5)
	if err := json.Unmarshal(r.Metadata, &o.Metadata); err != nil {
		return Object{}, fmt.Errorf("%w: the metadata of %q: %w", ErrUnavailable, r.Name, err)
	}
	tokens, err := s.openTokens(b.Project, r.Name, r.DownloadTokens)
	if err != nil {
		s.log.Error("storage: an object's download tokens do not open", "project", b.Project, "name", r.Name, "err", err)
		return Object{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	o.DownloadTokens = tokens
	return o, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
