package core

import (
	"context"
	"errors"
	"sync"
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
//     documents after the writes (plan.apply);
//  5. the second batch, one round trip: (F2) the index entries, then the stamp statement, which
//     takes the sequencer row, then COMMIT;
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

	mu    sync.Mutex
	gates map[dbKey]chan struct{}
}

func newSequencer(pool *pgxpool.Pool, size int, notify Notifier) *sequencer {
	return &sequencer{pool: pool, size: max(size, 1), notify: notify, gates: map[dbKey]chan struct{}{}}
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
		if !errors.Is(err, errRetry) {
			return res, err
		}
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
	insertKeys, insertNames, insertParentHashes, insertCgHashes [][]byte
	insertParents, insertDocs                                   [][]byte
	insertCollectionIDs                                         []string
	insertSizes                                                 []int32
	deleteKeys                                                  [][]byte
	deleteSeqs                                                  []int64
	replaced                                                    [][]byte
	logKeys, logNames, logBefores, logAfters, logMasks          [][]byte
}

func (s *sequencer) attempt(ctx context.Context, db DB, p *plan) (CommitResult, error) {
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
			var key, doc []byte
			d := &lockedDoc{}
			if err := rows.Scan(&key, &d.seq, &doc, &d.createTime, &d.updateTime); err != nil {
				return err
			}
			var err error
			if d.fields, _, _, err = decodeDocument(doc); err != nil {
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

	if p.verify != nil {
		if err := p.verify(locked); err != nil {
			rollback(conn)
			return CommitResult{}, err
		}
	}
	if err := p.apply(db, locked); err != nil {
		rollback(conn)
		return CommitResult{}, err
	}
	var in stampInput
	if err := p.stampArgs(&in); err != nil {
		rollback(conn)
		return CommitResult{}, err
	}
	if len(in.logKeys) == 0 {
		// Nothing changes: no seq, and the commit_time is the last commit's, which is at or
		// after every update_time the commit read (ReadCatalog ran after the locks).
		if _, err := conn.Exec(ctx, "COMMIT"); err != nil {
			rollback(conn)
			return CommitResult{}, failed(err)
		}
		return CommitResult{CommitTime: catalog.LastTs, Results: p.results(catalog.LastTs)}, nil
	}

	// The second batch, under the sequencer row from the stamp to COMMIT. It runs to the end
	// whatever the caller does: a cancelled request must not leave an unknown outcome.
	var seq int64
	var commitTime time.Time
	stamped := false
	b = &pgx.Batch{}
	// F2 queues the index entries' DeleteIndexEntries and InsertIndexEntries here, before the stamp.
	stampSQL, stampArgs := statement(func(q *queries.Queries) error {
		_, err := q.StampCommit(ctx, queries.StampCommitParams{
			ProjectNumber: db.Project, DatabaseID: db.ID, CatalogVersion: catalog.CatalogVersion,
			UpdateKeys: in.updateKeys, UpdateSeqs: in.updateSeqs, UpdateDocs: in.updateDocs, UpdateSizes: in.updateSizes,
			InsertKeys: in.insertKeys, InsertNames: in.insertNames, InsertParentHashes: in.insertParentHashes,
			InsertCgHashes: in.insertCgHashes, InsertParents: in.insertParents, InsertCollectionIds: in.insertCollectionIDs,
			InsertDocs: in.insertDocs, InsertSizes: in.insertSizes,
			DeleteKeys: in.deleteKeys, DeleteSeqs: in.deleteSeqs, ReplacedKeys: in.replaced,
			LogKeys: in.logKeys, LogNames: in.logNames, LogBefores: in.logBefores, LogAfters: in.logAfters, LogMasks: in.logMasks,
		})
		return err
	})
	b.Queue(stampSQL, stampArgs...).Query(func(rows pgx.Rows) error {
		for rows.Next() {
			var ok bool
			if err := rows.Scan(&seq, &commitTime, &ok); err != nil {
				return err
			}
			stamped = true
		}
		return rows.Err()
	})
	b.Queue("COMMIT")
	err = conn.SendBatch(context.WithoutCancel(ctx), b).Close()
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
	case "55P03", "57014": // lock_not_available (lock_timeout), query_canceled (statement_timeout)
		return ErrContention
	}
	return err
}
