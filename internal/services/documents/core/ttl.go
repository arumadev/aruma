package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/queries"
	"github.com/arumadev/aruma/internal/services/documents/value"
)

// TTL policies (ADR-024 §1.2, §2.5; ADR-028 §2). A policy names one field of one collection
// group. Every commit stores a document's expiry in documents.expire_time from its group's
// policies, CREATING and ACTIVE, and the field build (indexbuild.go) fills the documents already
// there before the policy is ACTIVE, or clears them when it is removed. The sweeper deletes the
// documents due under an ACTIVE policy, each through the commit path, so that listeners and the
// changelog see a normal delete.
//
// What expires, from https://firebase.google.com/docs/firestore/ttl (not recorded: the rules
// forbid changing the real project's settings):
//   - "For Standard edition databases, the field must be set to a Date and time value."; "Using
//     any other data type or not setting the TTL field value will disable the TTL for the
//     individual document." An array expires only in an Enterprise database, which this is not.
//   - "Data is typically deleted within 24 hours after its expiration date", and an expired
//     document is read and queried as any other until then.
//   - "Deleting a document through TTL does not delete subcollections under that document."
//   - "Deletion through TTL calls all active snapshot listeners and triggers".
//   - "You can mark only one field per collection group as a TTL field." The Admin API does not
//     refuse a second (production's answer is not recorded); a document then expires at the
//     earliest of its group's policies.

// ttlPolicy is one TTL policy of a collection group.
type ttlPolicy struct {
	path   value.FieldPath
	active bool
}

// addTTL adds a field override's TTL policy to the catalog.
func (c *catalog) addTTL(r overrideRow) error {
	p, err := value.ParseFieldPath(r.path)
	if err != nil {
		return err
	}
	if c.ttl == nil {
		c.ttl = map[string][]ttlPolicy{}
	}
	c.ttl[r.group] = append(c.ttl[r.group], ttlPolicy{path: p, active: r.ttlState == "ACTIVE"})
	return nil
}

// expiry returns when a document of path with fields expires under the catalog's TTL policies,
// the ACTIVE ones alone when active is set: the earliest Timestamp its group's policy fields
// hold. ok is false when none holds one.
func (c *catalog) expiry(path Path, fields value.Value, active bool) (t time.Time, ok bool) {
	for _, pol := range c.ttl[path.CollectionID()] {
		if active && !pol.active {
			continue
		}
		v, found := lookup(fields, pol.path)
		if !found || v.Kind() != value.KindTimestamp {
			continue
		}
		sec, nanos := v.TimestampValue()
		if e := time.Unix(sec, int64(nanos)).UTC(); !ok || e.Before(t) {
			t, ok = e, true
		}
	}
	return t, ok
}

// minStoredExpiry is the earliest expire_time stored: the zero time.Time stands for none in the
// statements' arrays (queries.WriteCommit), so an expiry within its second is stored a second
// later, long due either way.
var minStoredExpiry = time.Time{}.Add(time.Second)

// storedExpiry is a document's expire_time as the statements take it: the zero time for none.
func (c *catalog) storedExpiry(path Path, fields value.Value) time.Time {
	t, ok := c.expiry(path, fields, false)
	if !ok {
		return time.Time{}
	}
	if t.Before(minStoredExpiry) {
		return minStoredExpiry
	}
	return t
}

// TTLOptions times the TTL sweeper; the zero value is the defaults.
type TTLOptions struct {
	// Every is how often a pass runs (documents.limits.ttl_sweep_interval). Default 1 hour.
	Every time.Duration
	// Batch is the due documents one read of a database takes (documents.limits.ttl_sweep_batch).
	// Default 500.
	Batch int
	// MaxBatches bounds a pass's batches per database, so that one database's backlog neither
	// holds the pass nor starves the others; the rest waits for the next pass. Default 100.
	MaxBatches int
	Log        *slog.Logger

	// now, set by tests, is the clock.
	now func() time.Time
}

func (o TTLOptions) withDefaults() TTLOptions {
	o.Every = cmp.Or(o.Every, time.Hour)
	o.Batch = cmp.Or(o.Batch, 500)
	o.MaxBatches = cmp.Or(o.MaxBatches, 100)
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.now == nil {
		o.now = time.Now
	}
	return o
}

// Swept counts what a sweep did.
type Swept struct {
	// Deleted is the documents deleted; Skipped the due ones not deleted: rewritten or deleted
	// since they were read, no longer due, or their policy not ACTIVE.
	Deleted, Skipped int64
}

// ttlStats counts the sweeper's passes and deletes since the store opened.
type ttlStats struct {
	passes, deleted, skipped, failed atomic.Int64
}

// TTLStats is the sweeper's counts since the store opened.
type TTLStats struct {
	Passes, Deleted, Skipped, Failed int64
}

// TTLStats returns the sweeper's counts since the store opened.
func (s *PGStore) TTLStats() TTLStats {
	return TTLStats{Passes: s.ttl.passes.Load(), Deleted: s.ttl.deleted.Load(), Skipped: s.ttl.skipped.Load(), Failed: s.ttl.failed.Load()}
}

// RunTTL runs a sweep every o.Every until ctx ends, while this process holds the single-instance
// lock (ADR-024 §7). Each pass is logged and counted (TTLStats).
func (s *PGStore) RunTTL(ctx context.Context, o TTLOptions) {
	o = o.withDefaults()
	t := time.NewTicker(o.Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if s.serving() != nil {
			continue
		}
		start := time.Now()
		n, err := s.SweepExpired(ctx, o)
		if err != nil && ctx.Err() == nil {
			s.ttl.failed.Add(1)
			o.Log.Error("documents: TTL sweep", "error", err, "deleted", n.Deleted, "skipped", n.Skipped)
			continue
		}
		o.Log.Info("documents: TTL sweep", "deleted", n.Deleted, "skipped", n.Skipped, "duration", time.Since(start))
	}
}

// SweepExpired runs one pass over every database with an ACTIVE TTL policy: the documents due by
// expire_time, o.Batch at a time and at most o.MaxBatches batches per database, each checked
// again against its fields and the current catalog and deleted by its own commit, with a
// precondition that its update_time is the one read, so a document rewritten since is kept. It
// returns ErrNotServing, with what it did, when the process does not hold the lock.
func (s *PGStore) SweepExpired(ctx context.Context, o TTLOptions) (Swept, error) {
	o = o.withDefaults()
	var n Swept
	s.ttl.passes.Add(1)
	defer func() {
		s.ttl.deleted.Add(n.Deleted)
		s.ttl.skipped.Add(n.Skipped)
	}()
	if err := s.serving(); err != nil {
		return n, err
	}
	dbs, err := s.q.TTLDatabases(ctx)
	if err != nil {
		return n, fmt.Errorf("documents: TTL sweep: list the databases: %w", err)
	}
	for _, r := range dbs {
		db := DB{Project: r.ProjectNumber, ID: r.DatabaseID}
		if err := s.sweepDatabase(ctx, db, o, &n); err != nil {
			return n, fmt.Errorf("documents: TTL sweep of %d/%s: %w", db.Project, db.ID, err)
		}
	}
	return n, nil
}

// sweepDatabase deletes db's due documents, up to o.MaxBatches batches.
func (s *PGStore) sweepDatabase(ctx context.Context, db DB, o TTLOptions, n *Swept) error {
	now := o.now()
	var afterExpire time.Time
	afterKey := []byte{}
	for range o.MaxBatches {
		if err := s.serving(); err != nil {
			return err
		}
		cat, err := s.cats.get(ctx, db)
		if err != nil {
			return err
		}
		docs, err := s.q.DueDocuments(ctx, queries.DueDocumentsParams{ProjectNumber: db.Project, DatabaseID: db.ID,
			Now: now, AfterExpire: afterExpire, AfterKey: afterKey, Batch: int32(o.Batch)})
		if err != nil {
			return err
		}
		for _, d := range docs {
			deleted, err := s.expire(ctx, db, cat, d, now)
			if err != nil {
				return err
			}
			if deleted {
				n.Deleted++
			} else {
				n.Skipped++
			}
		}
		if len(docs) < o.Batch {
			return nil
		}
		last := docs[len(docs)-1]
		afterExpire, afterKey = last.ExpireTime, last.NameKey
	}
	return nil
}

// expire deletes a due document through the commit path, unless its fields under the catalog's
// ACTIVE policies say it is not due (expire_time may be stale while a policy is built), or it
// changed since it was read.
func (s *PGStore) expire(ctx context.Context, db DB, cat *catalog, d queries.DueDocumentsRow, now time.Time) (bool, error) {
	p, err := pathOfKey(d.Name)
	if err != nil {
		return false, err
	}
	fields, _, _, err := decodeDocument(d.Doc)
	if err != nil {
		return false, err
	}
	if t, ok := cat.expiry(p, fields, true); !ok || t.After(now) {
		return false, nil
	}
	_, err = s.Commit(ctx, db, []Write{{Kind: WriteDelete, Path: p, Precondition: Precondition{UpdateTime: d.UpdateTime}}})
	if e, ok := errors.AsType[*Error](err); ok && (e.Code == FailedPrecondition || e.Code == Aborted) {
		// Rewritten or deleted since it was read, or held by a transaction: the next pass
		// finds it again if it is still due.
		return false, nil
	}
	return err == nil, err
}
