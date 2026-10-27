package core

import (
	"context"
	"errors"
	"iter"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/services/documents/queries"
)

// The commit path (ADR-024 §2.1), for every commit of a database:
//
//  1. the lock manager's locks on the written documents, and a transaction's checks of what it
//     read (lockCommit);
//  2. the database's commit gate, then a pooled connection;
//  3. the first batch: BEGIN READ COMMITTED, the transaction's timeouts, the written documents'
//     rows FOR UPDATE in name-key order, and the catalog version and last commit_time;
//  4. in Go: a transaction's documents as it read them (plan.verify), the preconditions and the
//     documents after the writes (plan.apply), and the index entries they delete and insert
//     under the database's catalog (plan.indexArgs), which the commit read before the first
//     batch (catalogs, cached);
//  5. the second batch, one round trip: the index entries, then the stamp, which takes the
//     sequencer row and fails unless the catalog is still the commit's, the single-instance
//     lock's fence (FenceCommit), the documents and changelog (WriteCommit), then COMMIT. A commit with a REQUEST_TIME transform sends the
//     stamp alone, builds its documents and their entries with the commit_time it returned
//     (plan.apply again), then sends the rest; one that changes nothing takes no stamp, only a
//     commit_time from the clock (sequencer.unchanged);
//  6. after COMMIT returned, and only then, the hub is told (Notifier.Committed).
//
// The sequencer row is held from the stamp to the end of COMMIT, so a database's commits become
// visible in seq order, each with a commit_time from clock_timestamp() above the last.

const (
	// DefaultCommitConcurrency is the commits of one database inside Postgres at once
	// (documents.limits.commit_concurrency, ADR-024 §2.2): more queue on one row's lock.
	DefaultCommitConcurrency = 4
	// commitAttempts bounds the retries of a commit that met a concurrent change Postgres
	// detected (40001) or a catalog change.
	commitAttempts = 5
)

// commitSettings are the commit transaction's own timeouts (ADR-024 §2.3), SET LOCAL so that they
// leave the shared connection as they found it. A commit that holds the sequencer row and goes
// silent stops every commit of its database until the server ends its session.
var commitSettings = []string{
	"SET LOCAL idle_in_transaction_session_timeout = '5s'",
	"SET LOCAL statement_timeout = '10s'",
	"SET LOCAL lock_timeout = '5s'",
}

// Notifier hears of a database's commits: the hub (F3) releases events in seq order from it.
// Committed is called after COMMIT returned; Unknown when the connection was lost after COMMIT
// was sent, with the stamped seq, or 0 when the stamp's reply never came. A commit that changed
// nothing takes no seq and is not reported. Calls may come from many goroutines and in any order.
type Notifier interface {
	Committed(db DB, seq int64, commitTime time.Time, changes []Change)
	Unknown(db DB, seq int64)
}

type dbKey struct {
	project int64
	id      string
}

// sequencer runs commits: the gate per database and the two batches.
type sequencer struct {
	pool   *pgxpool.Pool
	size   int
	notify Notifier
	cats   *catalogs
	// instance, when set, is the single-instance lock every stamp is fenced on (FenceCommit).
	instance *Instance

	mu    sync.Mutex
	gates map[dbKey]chan struct{}

	// afterStamp, set by tests, runs between a REQUEST_TIME commit's stamp and its second batch.
	afterStamp func()
	// largeCommits counts the commits accepted whose entry changes, summed, pass
	// maxTransactionSize (PGStore.LargeCommits).
	largeCommits atomic.Int64
}

func newSequencer(pool *pgxpool.Pool, size int, notify Notifier, cats *catalogs, instance *Instance) *sequencer {
	return &sequencer{pool: pool, size: max(size, 1), notify: notify, cats: cats, instance: instance, gates: map[dbKey]chan struct{}{}}
}

func (s *sequencer) gate(db DB) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := dbKey{db.Project, db.ID}
	g := s.gates[k]
	if g == nil {
		g = make(chan struct{}, s.size)
		s.gates[k] = g
	}
	return g
}

var (
	// errRetry is a commit Postgres rolled back for a concurrent change, or whose catalog changed
	// under it: computed again from its first batch.
	errRetry = errors.New("documents: commit must be computed again")
	// errNoDatabase is a commit on a database without a sequencer row.
	errNoDatabase = errors.New("documents: no such database")
)

// commit runs p as one commit of db, computing it again on errRetry.
func (s *sequencer) commit(ctx context.Context, db DB, p *plan) (CommitResult, error) {
	for attempt := 1; ; attempt++ {
		res, err := s.attempt(ctx, db, p)
		if errors.Is(err, ErrNotServing) && s.instance != nil {
			// The fence found the lock gone before the guard did.
			s.instance.suspect()
		}
		if !errors.Is(err, errRetry) {
			return res, err
		}
		// The catalog may be what changed: read it again.
		s.cats.drop(db)
		if attempt == commitAttempts {
			return CommitResult{}, ErrContention
		}
	}
}

// stampInput is the stamp statement's arrays (queries.StampCommitParams).
type stampInput struct {
	updateKeys, updateDocs                                      [][]byte
	updateSeqs                                                  []int64
	updateSizes                                                 []int32
	updateExpires, insertExpires                                []time.Time
	insertKeys, insertNames, insertParentHashes, insertCgHashes [][]byte
	insertParents, insertDocs                                   [][]byte
	insertCollectionIDs                                         []string
	insertSizes                                                 []int32
	deleteKeys                                                  [][]byte
	deleteSeqs                                                  []int64
	replaced                                                    [][]byte
	logKeys, logNames, logBefores, logAfters, logMasks          [][]byte
	index                                                       indexChanges
}

func (s *sequencer) attempt(ctx context.Context, db DB, p *plan) (CommitResult, error) {
	// The catalog the commit's index entries are computed with; the stamp checks it is current.
	cat, err := s.cats.get(ctx, db)
	if err != nil {
		return CommitResult{}, err
	}
	g := s.gate(db)
	select {
	case g <- struct{}{}:
	case <-ctx.Done():
		return CommitResult{}, ctx.Err()
	}
	defer func() { <-g }()
	pc, err := s.pool.Acquire(ctx)
	if err != nil {
		return CommitResult{}, err
	}
	// Release destroys a connection left in a transaction or closed.
	defer pc.Release()
	conn := pc.Conn()

	// The first batch: open, lock, read the catalog.
	locked := map[string]*lockedDoc{}
	var catalog queries.ReadCatalogRow
	found := false
	b := &pgx.Batch{}
	b.Queue("BEGIN ISOLATION LEVEL READ COMMITTED")
	for _, set := range commitSettings {
		b.Queue(set)
	}
	lockSQL, lockArgs := statement(func(q *queries.Queries) error {
		_, err := q.LockDocuments(ctx, queries.LockDocumentsParams{ProjectNumber: db.Project, DatabaseID: db.ID, NameKeys: p.storedKeys()})
		return err
	})
	b.Queue(lockSQL, lockArgs...).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			var key []byte
			d := &lockedDoc{}
			if err := rows.Scan(&key, &d.seq, &d.stored, &d.createTime, &d.updateTime); err != nil {
				return err
			}
			locked[string(key)] = d
		}
		return rows.Err()
	})
	catalogSQL, catalogArgs := statement(func(q *queries.Queries) error {
		_, err := q.ReadCatalog(ctx, queries.ReadCatalogParams{ProjectNumber: db.Project, DatabaseID: db.ID})
		return err
	})
	b.Queue(catalogSQL, catalogArgs...).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			found = true
			if err := rows.Scan(&catalog.CatalogVersion, &catalog.LastTs); err != nil {
				return err
			}
		}
		return rows.Err()
	})
	if err := conn.SendBatch(ctx, b).Close(); err != nil {
		rollback(conn)
		return CommitResult{}, failed(err)
	}
	if !found {
		rollback(conn)
		return CommitResult{}, errNoDatabase
	}
	if !cat.found || catalog.CatalogVersion != cat.version {
		rollback(conn)
		return CommitResult{}, errRetry
	}
	// The documents are decoded once read, in parallel (plan.indexArgs).
	if err := decodeLocked(locked); err != nil {
		rollback(conn)
		return CommitResult{}, err
	}

	if p.verify != nil {
		if err := p.verify(locked); err != nil {
			rollback(conn)
			return CommitResult{}, err
		}
	}
	// The documents after the writes. A plan with REQUEST_TIME is folded here with the last
	// commit's time, for its errors and to know what it changes; its documents are built again
	// once the commit is stamped.
	if err := p.apply(db, locked, catalog.LastTs); err != nil {
		rollback(conn)
		return CommitResult{}, err
	}
	var in stampInput
	if err := p.stampArgs(&in, cat); err != nil {
		rollback(conn)
		return CommitResult{}, err
	}
	if len(in.logKeys) == 0 {
		return s.unchanged(ctx, conn, db, p, locked)
	}
	// A REQUEST_TIME commit's entries are known once it is stamped; its limits are checked
	// here, since the time it holds changes no entry's size nor number.
	if in.index, err = p.indexArgs(cat); err != nil {
		rollback(conn)
		return CommitResult{}, err
	}
	res, err := s.stamp(ctx, conn, db, p, locked, cat, &in)
	if err == nil && in.index.size > maxTransactionSize {
		// Production refuses one document's changes past the bound; whether it refuses a
		// commit's sum is not recorded (indexentries.go), so this one is accepted and counted.
		s.largeCommits.Add(1)
		slog.Warn("documents: a commit's index entry changes pass the bound production holds one document to",
			"project", db.Project, "database", db.ID, "documents", len(p.docs), "size", in.index.size, "bound", maxTransactionSize)
	}
	return res, err
}

// decodeLocked decodes the fields of the documents the first batch locked.
func decodeLocked(locked map[string]*lockedDoc) error {
	docs := slices.Collect(maps.Values(locked))
	errs := make([]error, len(docs))
	parallel(len(docs), func(i int) {
		docs[i].fields, _, _, errs[i] = decodeDocument(docs[i].stored)
	})
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// unchanged ends a commit that changes nothing: no seq, and a commit_time from the clock
// (queries.UnchangedCommitTime), which production's advances too.
func (s *sequencer) unchanged(ctx context.Context, conn *pgx.Conn, db DB, p *plan, locked map[string]*lockedDoc) (CommitResult, error) {
	var commitTime time.Time
	b := &pgx.Batch{}
	sql, args := statement(func(q *queries.Queries) error {
		_, err := q.UnchangedCommitTime(ctx, queries.UnchangedCommitTimeParams{ProjectNumber: db.Project, DatabaseID: db.ID})
		return err
	})
	b.Queue(sql, args...).QueryRow(func(row pgx.Row) error { return row.Scan(&commitTime) })
	b.Queue("COMMIT")
	if err := conn.SendBatch(ctx, b).Close(); err != nil {
		rollback(conn)
		return CommitResult{}, failed(err)
	}
	if p.serverTime {
		// The transform results of writes whose document ends as it was (deleted after).
		if err := p.apply(db, locked, commitTime); err != nil {
			return CommitResult{}, err
		}
	}
	return CommitResult{CommitTime: commitTime, Results: p.results(commitTime)}, nil
}

// stamp runs the second batch, under the sequencer row from the stamp to COMMIT: the stamp, the
// documents and COMMIT in one round trip. A plan with REQUEST_TIME sends the stamp alone, builds
// its documents with the commit_time it returned, then sends the rest. It runs to the end
// whatever the caller does: a cancelled request must not leave an unknown outcome.
func (s *sequencer) stamp(ctx context.Context, conn *pgx.Conn, db DB, p *plan, locked map[string]*lockedDoc, cat *catalog, in *stampInput) (CommitResult, error) {
	ctx = context.WithoutCancel(ctx)
	catalogVersion := cat.version
	var seq int64
	var commitTime time.Time
	stamped := false
	b := &pgx.Batch{}
	if !p.serverTime {
		// Before the stamp: their cost falls outside the sequencer row (ADR-024 §2.1).
		queueIndexChanges(ctx, b, db, &in.index)
	}
	stampSQL, stampArgs := statement(func(q *queries.Queries) error {
		_, err := q.StampCommit(ctx, queries.StampCommitParams{ProjectNumber: db.Project, DatabaseID: db.ID, CatalogVersion: catalogVersion})
		return err
	})
	b.Queue(stampSQL, stampArgs...).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			if err := rows.Scan(&seq, &commitTime); err != nil {
				return err
			}
			stamped = true
		}
		return rows.Err()
	})
	if s.instance != nil {
		h := s.instance.fence()
		sql, args := statement(func(q *queries.Queries) error {
			return q.FenceCommit(ctx, queries.FenceCommitParams{Holder: h.pid, HolderStart: h.start})
		})
		b.Queue(sql, args...)
	}
	if p.serverTime {
		if err := conn.SendBatch(ctx, b).Close(); err != nil {
			rollback(conn)
			return CommitResult{}, failed(err)
		}
		if !stamped {
			// The catalog changed since the first batch.
			rollback(conn)
			return CommitResult{}, errRetry
		}
		if s.afterStamp != nil {
			s.afterStamp()
		}
		*in = stampInput{}
		if err := p.apply(db, locked, commitTime); err != nil {
			rollback(conn)
			return CommitResult{}, err
		}
		if err := p.stampArgs(in, cat); err != nil {
			rollback(conn)
			return CommitResult{}, err
		}
		var err error
		if in.index, err = p.indexArgs(cat); err != nil {
			rollback(conn)
			return CommitResult{}, err
		}
		b = &pgx.Batch{}
		queueIndexChanges(ctx, b, db, &in.index)
	}
	writeSQL, writeArgs := statement(func(q *queries.Queries) error {
		_, err := q.WriteCommit(ctx, queries.WriteCommitParams{
			ProjectNumber: db.Project, DatabaseID: db.ID, CatalogVersion: catalogVersion,
			UpdateKeys: in.updateKeys, UpdateSeqs: in.updateSeqs, UpdateDocs: in.updateDocs, UpdateSizes: in.updateSizes, UpdateExpires: in.updateExpires,
			InsertKeys: in.insertKeys, InsertNames: in.insertNames, InsertParentHashes: in.insertParentHashes,
			InsertCgHashes: in.insertCgHashes, InsertParents: in.insertParents, InsertCollectionIds: in.insertCollectionIDs,
			InsertDocs: in.insertDocs, InsertSizes: in.insertSizes, InsertExpires: in.insertExpires,
			DeleteKeys: in.deleteKeys, DeleteSeqs: in.deleteSeqs, ReplacedKeys: in.replaced,
			LogKeys: in.logKeys, LogNames: in.logNames, LogBefores: in.logBefores, LogAfters: in.logAfters, LogMasks: in.logMasks,
		})
		return err
	})
	b.Queue(writeSQL, writeArgs...)
	b.Queue("COMMIT")
	err := conn.SendBatch(ctx, b).Close()
	switch {
	case err == nil && !stamped:
		// The catalog changed since the first batch; the COMMIT committed nothing.
		return CommitResult{}, errRetry
	case err == nil:
		if s.notify != nil {
			s.notify.Committed(db, seq, commitTime, p.changes(commitTime))
		}
		return CommitResult{CommitTime: commitTime, Results: p.results(commitTime), Seq: seq}, nil
	case isPgError(err):
		// Postgres refused a statement: the transaction is aborted and COMMIT did not run.
		rollback(conn)
		return CommitResult{}, failed(err)
	case pgconn.SafeToRetry(err):
		// Nothing of the batch reached the server.
		return CommitResult{}, err
	}
	if s.notify != nil {
		s.notify.Unknown(db, seq)
	}
	return CommitResult{}, ErrUnknownOutcome
}

// indexStatementEntries and indexStatementBytes bound the entries one index statement sends.
// statement_timeout holds each statement of the commit, not the batch (ADR-024 §2.3): a commit
// of 500 wide documents changes some 200,000 entries, which go in statements of a few hundred
// milliseconds each, in one round trip still, and in the order they were sorted in.
const (
	indexStatementEntries = 20_000
	indexStatementBytes   = 8 << 20
)

// queueIndexChanges queues a commit's index statements, those it has: the deletes, then the
// inserts, each in chunks (indexStatementEntries).
func queueIndexChanges(ctx context.Context, b *pgx.Batch, db DB, c *indexChanges) {
	for i, j := range indexChunks(c.delKeys, nil) {
		sql, args := statement(func(q *queries.Queries) error {
			return q.DeleteIndexEntries(ctx, queries.DeleteIndexEntriesParams{ProjectNumber: db.Project, DatabaseID: db.ID,
				IndexIds: c.delIDs[i:j], Keys: c.delKeys[i:j]})
		})
		b.Queue(sql, args...)
	}
	for i, j := range indexChunks(c.insKeys, c.insFullKeys) {
		sql, args := statement(func(q *queries.Queries) error {
			return q.InsertIndexEntries(ctx, queries.InsertIndexEntriesParams{ProjectNumber: db.Project, DatabaseID: db.ID,
				IndexIds: c.insIDs[i:j], Keys: c.insKeys[i:j], FullKeys: c.insFullKeys[i:j]})
		})
		b.Queue(sql, args...)
	}
}

// indexChunks yields the bounds [i, j) of consecutive chunks of entries, keys and their full
// keys (nil for none), each within indexStatementEntries and, but for a single entry,
// indexStatementBytes.
func indexChunks(keys, full [][]byte) iter.Seq2[int, int] {
	return func(yield func(int, int) bool) {
		i, size := 0, 0
		for j, k := range keys {
			n := len(k)
			if full != nil {
				n += len(full[j])
			}
			if j > i && (j-i == indexStatementEntries || size+n > indexStatementBytes) {
				if !yield(i, j) {
					return
				}
				i, size = j, 0
			}
			size += n
		}
		if i < len(keys) {
			yield(i, len(keys))
		}
	}
}

// rollback ends a failed commit's transaction; a closed connection has none left.
func rollback(conn *pgx.Conn) {
	if !conn.IsClosed() {
		_, _ = conn.Exec(context.Background(), "ROLLBACK")
	}
}

func isPgError(err error) bool {
	_, ok := errors.AsType[*pgconn.PgError](err)
	return ok
}

// failed maps a commit's settled failure: a concurrent change Postgres detected is computed
// again, a timeout is contention (ABORTED, which clients retry); anything else is the server's.
func failed(err error) error {
	pe, ok := errors.AsType[*pgconn.PgError](err)
	if !ok {
		return err
	}
	switch pe.Code {
	case "40001", "40P01": // serialization_failure (conflict_unless), deadlock_detected
		return errRetry
	case "AR001": // documents.instance_fence: the single-instance lock is no longer this process's
		return ErrNotServing
	case "55P03", "57014": // lock_not_available (lock_timeout), query_canceled (statement_timeout)
		return ErrContention
	case "25P03":
		// idle_in_transaction_session_timeout: the server ended the session while it waited for
		// the commit's next batch, as it may between a REQUEST_TIME commit's stamp and its
		// second batch, so COMMIT never ran (MaxCommitWork bounds the work between them).
		return ErrContention
	}
	return err
}
