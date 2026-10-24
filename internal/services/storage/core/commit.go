package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/services/storage/queries"
)

// The one transaction that makes a file visible (ADR-022 §5), and the write order around it:
//
//  1. a new key p/<n>/<uuid> that no row points to;
//  2. the file written there and committed (fsync of the file and its directory), or a staging
//     file promoted to it;
//  3. commitObject: the object's row with a new generation, the replaced file into blob_gc
//     (releaseObject), the quota moved from reserved to used, and for an upload its session made
//     final, in one transaction that commits synchronously (begin, durability.go);
//  4. the GC worker deletes what blob_gc holds, after gc_grace (gc.go).
//
// An error before COMMIT is sent leaves nothing visible, and the caller deletes its new file at
// once. An error while COMMIT is in flight is ambiguous: the transaction may have committed, so
// nothing is deleted before pg_xact_status(xid), read on another connection, says it aborted.

// commitRetries bounds how often a write inserts again after a delete that committed between its
// insert's conflict and its lock (TestCommitStatementsRetryAfterConcurrentDelete).
const commitRetries = 5

// errCommitUnknown is a COMMIT whose outcome could not be settled. The new file stays: a row may
// point to it. A session's next finalize settles it (its row holds blob_key); otherwise Boot's
// sweep deletes it once no row points to it.
var errCommitUnknown = errors.New("storage: the commit's outcome is unknown")

// testHook, when set, runs at the named points of the write order; tests crash there (a panic
// is a process that dies at that instant) or race another write in.
var testHook func(point string)

func hook(point string) {
	if testHook != nil {
		testHook(point)
	}
}

// commitTx sends COMMIT; a test replaces it to lose the reply of a commit that happened.
var commitTx = func(ctx context.Context, tx pgx.Tx) error { return tx.Commit(ctx) }

// xactStatusOf, when set, replaces the pg_xact_status lookup in tests.
var xactStatusOf func(ctx context.Context, xid string) (string, error)

// commitObject is the one transaction that makes a file visible (ADR-022 §5): the object's row
// with a new generation, the replaced file into blob_gc, and the quota moved from reserved to
// used, reading the replaced row under its lock in the transaction that writes it.
//
// It inserts with DO NOTHING (alone under ifGenerationMatch=0, so a second create fails with
// 412), and on a conflict locks the row (FOR UPDATE), checks the preconditions on it and
// replaces it. A missing row after the conflict is a delete that committed in between: the insert
// runs again, a bounded number of times, never 412. An error it returns other than
// errCommitUnknown left nothing visible.
func (s *Service) commitObject(ctx context.Context, w objectWrite) (Object, error) {
	o := w.object
	project := w.bucket.Project
	tokens, err := s.sealTokens(project, o.Name, o.DownloadTokens)
	if err != nil {
		return Object{}, err
	}
	md, err := encodeMetadata(o.Metadata)
	if err != nil {
		return Object{}, err
	}
	now := s.now()
	tx, err := s.begin(ctx)
	if err != nil {
		return Object{}, err
	}
	sent := false
	defer func() {
		if !sent {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	q := queries.New(tx)
	row, old, err := s.writeObjectRow(ctx, q, w, md, tokens, now)
	if err != nil {
		return Object{}, err
	}
	if old != nil {
		if err := releaseObject(ctx, q, project, old.BlobKey, old.Size); err != nil {
			return Object{}, err
		}
	}
	var anonymous int64
	var sess queries.StorageUploadSession
	if w.session != nil {
		if sess, err = q.GetUploadSession(ctx, queries.GetUploadSessionParams{IDHash: w.session, ProjectNumber: project}); err != nil {
			return Object{}, fmt.Errorf("%w: the upload session: %w", ErrUnavailable, err)
		}
		if sess.PrincipalKind == kindAnonymous {
			anonymous = w.reserved
		}
	}
	if err := q.EnsureUsage(ctx, project); err != nil {
		return Object{}, fmt.Errorf("%w: usage: %w", ErrUnavailable, err)
	}
	if err := q.CommitUsage(ctx, queries.CommitUsageParams{N: w.reserved, AnonymousN: anonymous, Size: row.Size,
		ProjectNumber: project}); err != nil {
		return Object{}, fmt.Errorf("%w: usage: %w", ErrUnavailable, err)
	}
	obj, err := s.rowObject(w.bucket, row, o.DownloadTokens)
	if err != nil {
		return Object{}, err
	}
	if w.session != nil {
		if err := s.finishSession(ctx, q, sess, obj); err != nil {
			return Object{}, err
		}
	}
	var xid string
	if err := tx.QueryRow(ctx, "SELECT pg_current_xact_id()::text").Scan(&xid); err != nil {
		return Object{}, fmt.Errorf("%w: read the transaction id: %w", ErrUnavailable, err)
	}
	hook("commit.before")
	sent = true
	if err := commitTx(context.WithoutCancel(ctx), tx); err != nil {
		return s.settle(ctx, xid, obj, err)
	}
	hook("commit.after")
	return obj, nil
}

// writeObjectRow writes the object's row: InsertObject, or on a conflict LockObject and
// ReplaceObject. It returns the row written and, for an overwrite, the row it replaced.
func (s *Service) writeObjectRow(ctx context.Context, q *queries.Queries, w objectWrite, md json.RawMessage,
	tokens []byte, now time.Time) (queries.StorageObject, *queries.StorageObject, error) {
	o := w.object
	project := w.bucket.Project
	// A precondition other than ifGenerationMatch=0 needs an object to hold on: no insert.
	mustExist := w.pre.IfGenerationNotMatch != nil || w.pre.IfMetagenerationMatch != nil ||
		w.pre.IfMetagenerationNotMatch != nil || (w.pre.IfGenerationMatch != nil && *w.pre.IfGenerationMatch != 0)
	for range commitRetries {
		if !mustExist {
			row, err := q.InsertObject(ctx, queries.InsertObjectParams{
				ProjectNumber: project, Name: o.Name, Generation: now.UnixMicro(), BlobKey: w.blobKey, Size: o.Size,
				Md5: o.MD5[:], Crc32c: int64(o.CRC32C), ContentType: optional(o.ContentType),
				ContentEncoding: optional(o.ContentEncoding), ContentDisposition: optional(o.ContentDisposition),
				ContentLanguage: optional(o.ContentLanguage), CacheControl: optional(o.CacheControl),
				Metadata: md, DownloadTokens: tokens, Now: now,
			})
			switch {
			case err == nil:
				return row, nil, nil
			case !errors.Is(err, pgx.ErrNoRows):
				return queries.StorageObject{}, nil, fmt.Errorf("%w: insert the object: %w", ErrUnavailable, err)
			case w.pre.IfGenerationMatch != nil:
				// ifGenerationMatch=0 on a name that exists.
				return queries.StorageObject{}, nil, ErrPreconditionFailed
			}
		}
		hook("commit.conflict")
		old, err := q.LockObject(ctx, queries.LockObjectParams{ProjectNumber: project, Name: o.Name})
		switch {
		case errors.Is(err, pgx.ErrNoRows) && mustExist:
			return queries.StorageObject{}, nil, ErrPreconditionFailed
		case errors.Is(err, pgx.ErrNoRows):
			continue // deleted since the insert's conflict: insert again
		case err != nil:
			return queries.StorageObject{}, nil, fmt.Errorf("%w: lock the object: %w", ErrUnavailable, err)
		}
		if err := checkPreconditions(w.pre, &old); err != nil {
			return queries.StorageObject{}, nil, err
		}
		row, err := q.ReplaceObject(ctx, queries.ReplaceObjectParams{
			Generation: now.UnixMicro(), BlobKey: w.blobKey, Size: o.Size, Md5: o.MD5[:], Crc32c: int64(o.CRC32C),
			ContentType: optional(o.ContentType), ContentEncoding: optional(o.ContentEncoding),
			ContentDisposition: optional(o.ContentDisposition), ContentLanguage: optional(o.ContentLanguage),
			CacheControl: optional(o.CacheControl), Metadata: md, DownloadTokens: tokens, Now: now,
			ProjectNumber: project, Name: o.Name,
		})
		if err != nil {
			return queries.StorageObject{}, nil, fmt.Errorf("%w: replace the object: %w", ErrUnavailable, err)
		}
		return row, &old, nil
	}
	return queries.StorageObject{}, nil, fmt.Errorf("%w: the object kept changing under the write", ErrUnavailable)
}

// checkPreconditions checks GCS's preconditions on the current row, nil when there is none
// (B §10.2-8). Without an object only ifGenerationMatch=0 holds: the others need one to compare.
func checkPreconditions(pre Preconditions, row *queries.StorageObject) error {
	if row == nil {
		if pre.IfGenerationNotMatch != nil || pre.IfMetagenerationMatch != nil || pre.IfMetagenerationNotMatch != nil ||
			(pre.IfGenerationMatch != nil && *pre.IfGenerationMatch != 0) {
			return ErrPreconditionFailed
		}
		return nil
	}
	switch {
	case pre.IfGenerationMatch != nil && *pre.IfGenerationMatch != row.Generation,
		pre.IfGenerationNotMatch != nil && *pre.IfGenerationNotMatch == row.Generation,
		pre.IfMetagenerationMatch != nil && *pre.IfMetagenerationMatch != row.Metageneration,
		pre.IfMetagenerationNotMatch != nil && *pre.IfMetagenerationNotMatch == row.Metageneration:
		return ErrPreconditionFailed
	}
	return nil
}

// settle decides a COMMIT whose reply was lost (ADR-022 §5): pg_xact_status on another
// connection says committed (the write stands) or aborted (nothing is visible, the caller deletes
// its file). Anything else, a server out of reach included, is errCommitUnknown and deletes
// nothing.
func (s *Service) settle(ctx context.Context, xid string, o Object, cause error) (Object, error) {
	status, err := s.xactStatus(ctx, xid)
	switch {
	case err == nil && status == "committed":
		s.metrics.AmbiguousCommits.WithLabelValues("committed").Inc()
		s.log.Warn("storage: a commit's reply was lost; it committed", "error", cause)
		return o, nil
	case err == nil && status == "aborted":
		s.metrics.AmbiguousCommits.WithLabelValues("aborted").Inc()
		return Object{}, fmt.Errorf("%w: the commit aborted: %w", ErrUnavailable, cause)
	}
	s.metrics.AmbiguousCommits.WithLabelValues("unknown").Inc()
	s.log.Error("storage: a commit's outcome is unknown; its file stays for the sweep", "error", cause, "status_error", err)
	return Object{}, fmt.Errorf("%w: %w: %w", ErrUnavailable, errCommitUnknown, cause)
}

// xactStatus asks for a transaction's outcome on a fresh connection, waiting a little while it
// is still in progress (a COMMIT the server finishes after the client went).
func (s *Service) xactStatus(ctx context.Context, xid string) (string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for i := 0; ; i++ {
		var status string
		var err error
		if xactStatusOf != nil {
			status, err = xactStatusOf(ctx, xid)
		} else {
			var v *string
			err = s.pool.QueryRow(ctx, "SELECT pg_xact_status($1::xid8)", xid).Scan(&v)
			if v != nil {
				status = *v
			}
		}
		if err != nil || status != "in progress" || i == 4 {
			return status, err
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// rowObject is the Object a row of storage.objects holds, with the download tokens the caller
// already has in the clear.
func (s *Service) rowObject(b Bucket, row queries.StorageObject, tokens []string) (Object, error) {
	o := Object{
		Bucket: b.Name, Name: row.Name, Generation: row.Generation, Metageneration: row.Metageneration,
		Size: row.Size, CRC32C: uint32(row.Crc32c),
		ContentType: deref(row.ContentType), ContentEncoding: deref(row.ContentEncoding),
		ContentDisposition: deref(row.ContentDisposition), ContentLanguage: deref(row.ContentLanguage),
		CacheControl: deref(row.CacheControl), DownloadTokens: tokens,
		Created: row.TimeCreated, Updated: row.Updated,
	}
	copy(o.MD5[:], row.Md5)
	if err := json.Unmarshal(row.Metadata, &o.Metadata); err != nil {
		return Object{}, fmt.Errorf("storage: the metadata of %q: %w", row.Name, err)
	}
	if len(o.Metadata) == 0 {
		o.Metadata = nil
	}
	return o, nil
}

// storedObject reads an object's row and opens its tokens; nil when there is none.
func (s *Service) storedObject(ctx context.Context, b Bucket, name string) (*Object, *queries.StorageObject, error) {
	row, err := s.reads.GetObject(ctx, queries.GetObjectParams{ProjectNumber: b.Project, Name: name})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil, nil
	case err != nil:
		return nil, nil, fmt.Errorf("%w: read the object: %w", ErrUnavailable, err)
	}
	tokens, err := s.openTokens(b.Project, name, row.DownloadTokens)
	if err != nil {
		return nil, nil, err
	}
	o, err := s.rowObject(b, row, tokens)
	if err != nil {
		return nil, nil, err
	}
	return &o, &row, nil
}

// encodeMetadata returns the metadata column: a JSON object of strings, {} when empty.
func encodeMetadata(md map[string]string) (json.RawMessage, error) {
	if len(md) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if _, ok := md[TokensKey]; ok {
		return nil, invalid("The custom metadata may not hold " + TokensKey + ".")
	}
	return json.Marshal(md)
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
