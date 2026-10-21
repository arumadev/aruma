package core

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/osama300/aurma-project/internal/services/rtdb/rules"
)

// Database is the single writer of one database (ADR-019): a goroutine that owns an inbox
// and applies commands in the order they arrive. It groups up to maxBatch waiting commands in
// one Postgres transaction, advances the database's seq in that same transaction, and only
// after the commit hands the changes to the commit hook and the replies to their callers.
// Every listener therefore sees one order of writes, the order of the database itself, and a
// change reaches listeners before its writer's ack. Reads go through the inbox too, so a
// connection reads its own writes.
type Database struct {
	project int64
	name    string
	store   Store
	inbox   chan *command
	now     func() time.Time
	log     *slog.Logger

	// onCommit receives each commit's changes before the replies go out. Listeners hang off
	// it (R3); it must not block.
	onCommit func(Commit)

	// The writer's own state.
	info   DatabaseInfo
	opened bool
	rate   rateWindow
	rules  *rules.Ruleset // nil when the stored rules do not parse, which denies all

	listeners *listeners

	// Guarded by the registry's lock.
	refs int
	gen  uint64 // counts acquisitions, so a stale retirement timer is recognised
	done chan struct{}
}

// Commit is what one transaction applied.
type Commit struct {
	// Seq is the database's write count after the commit; the changes are numbered
	// Seq-len(Changes)+1 to Seq.
	Seq     int64
	Changes []Change
}

// Result is a command's outcome. Node is the value read, or for a set the value written,
// server values resolved: the emulator acks a conditional write with the data, and REST echoes
// every set. Merge is an update's children as written. Warnings are a listen's (no_index).
type Result struct {
	Node     *Node
	Merge    []Child
	Err      error
	Warnings []string
}

// command is one inbox entry. Exactly one of write, register, listen, unlisten, revoke, drop
// and rules is set, or none for a read of read (with query when not nil), or a barrier.
type command struct {
	// acc is who the command acts for, checked against the rules; nil checks nothing.
	acc   *access
	write *Write
	read  Path
	query *Query
	// rest makes a read REST's: without a query, rules see a plain REST read
	// (rules.PlainREST). probe checks the rules of the read and reads nothing.
	rest, probe bool
	listen      *listenCmd
	unlisten    *unlistenCmd
	// register checks an onDisconnect write when it is registered, without applying it.
	register *Write
	// revoke re-checks a session's listens and cancels those no longer permitted.
	revoke *Session
	// drop removes the listens of a closing session.
	drop *Session
	// rules replaces the database's rules, and re-checks every listen against them.
	rules *rules.Ruleset
	// barrier does nothing: its done runs once every command queued before it has run.
	barrier bool
	done    func(Result)
}

type listenCmd struct {
	l    *listen
	hash string
}

type unlistenCmd struct {
	sess *Session
	key  string
}

const (
	// maxBatch caps the commands applied in one transaction.
	maxBatch = 64
	// inboxSize is how many commands may wait. A full inbox blocks the senders, which are
	// connection readers, so a busy database slows its own clients down and no one else.
	inboxSize = 1024
	// batchTimeout bounds one transaction.
	batchTimeout = 30 * time.Second
)

func newDatabase(project int64, name string, store Store, now func() time.Time, log *slog.Logger, limits Limits, onCommit func(Commit)) *Database {
	return &Database{
		project: project, name: name, store: store, now: now, log: log, onCommit: onCommit,
		inbox:     make(chan *command, inboxSize),
		rate:      rateWindow{limit: limits.WriteBytesPerMinute, start: now()},
		listeners: newListeners(),
		done:      make(chan struct{}),
	}
}

// Name is the database's namespace.
func (d *Database) Name() string { return d.name }

// Project is the number of the project the database belongs to.
func (d *Database) Project() int64 { return d.project }

// submit queues a command. done runs on the writer goroutine, after the commit; it must not
// block. Only holders of a reference submit, so the inbox is open.
func (d *Database) submit(c *command) { d.inbox <- c }

// run is the writer loop. It ends when the registry closes the inbox.
func (d *Database) run() {
	defer close(d.done)
	for c := range d.inbox {
		batch := []*command{c}
	more:
		for len(batch) < maxBatch {
			select {
			case c, ok := <-d.inbox:
				if !ok {
					break more
				}
				batch = append(batch, c)
			default:
				break more
			}
		}
		d.execute(batch)
	}
}

// open loads the database's row and its rules on the first batch.
func (d *Database) open(ctx context.Context) error {
	info, err := d.store.OpenDatabase(ctx, d.project, d.name)
	if err != nil {
		return err
	}
	d.info, d.opened = info, true
	d.rules = nil
	if rs, err := rules.Parse([]byte(info.Rules)); err == nil {
		d.rules = rs
	} else {
		// Saving rules checks them, so this is a database edited by hand. It stays locked to
		// clients, as a new database is, until valid rules are saved; without its .indexOn
		// every child and value query is served unfiltered.
		d.log.Error("rtdb: stored rules do not parse", "database", d.name, "error", err)
	}
	return nil
}

// execute applies a batch in one transaction. When the transaction fails, each command is
// retried on its own, so one bad command cannot fail the others.
func (d *Database) execute(batch []*command) {
	ctx, cancel := context.WithTimeout(context.Background(), batchTimeout)
	defer cancel()
	if !d.opened {
		if err := d.open(ctx); err != nil {
			d.log.Error("rtdb: open database", "database", d.name, "error", err)
			for _, c := range batch {
				c.done(Result{Err: ErrUnavailable})
			}
			return
		}
	}
	rate := d.rate
	state := &batchState{}
	tx, commit, results, err := d.apply(ctx, batch, state)
	if err != nil {
		// Nothing was committed, so the commands can run again, each on its own: one bad
		// command cannot fail the others.
		d.rate = rate
		state.rollback()
		if len(batch) > 1 {
			for _, c := range batch {
				d.execute([]*command{c})
			}
			return
		}
		var de *DataError
		if errors.As(err, &de) {
			batch[0].done(Result{Err: ErrInvalidData})
			return
		}
		d.log.Error("rtdb: write", "database", d.name, "error", err)
		batch[0].done(Result{Err: ErrUnavailable})
		return
	}
	if err := tx.Commit(ctx); err != nil && !d.committed(commit) {
		// The commit may or may not have happened. Running the commands again could apply
		// them twice, so they fail; the clients resend what they still want.
		d.rate = rate
		state.rollback()
		d.log.Error("rtdb: commit", "database", d.name, "error", err)
		for _, c := range batch {
			c.done(Result{Err: ErrUnavailable})
		}
		return
	}
	if len(commit.Changes) > 0 {
		d.info.Seq = commit.Seq
	}
	// Pushes go first, then the hook, then the replies: a writer's own listens get the data
	// before its ack, and a listen's data comes before its ok.
	for _, dl := range state.deliveries {
		if dl.sess.sink != nil {
			dl.sess.sink.Push(dl.push)
		}
	}
	if len(commit.Changes) > 0 && d.onCommit != nil {
		d.onCommit(commit)
	}
	for i, c := range batch {
		c.done(results[i])
	}
}

// committed settles a commit that returned an error: it took effect exactly when the
// database's seq, read afresh, is the one the transaction set. A batch without writes left
// no trace, so it counts as not committed. The writer's view of seq is refreshed either way.
func (d *Database) committed(c Commit) bool {
	ctx, cancel := context.WithTimeout(context.Background(), batchTimeout)
	defer cancel()
	seq, err := d.store.Seq(ctx, d.info)
	if err != nil {
		// Unknown still; reopen before the next batch to read seq again.
		d.opened = false
		return false
	}
	ok := len(c.Changes) > 0 && seq == c.Seq
	d.info.Seq = seq
	return ok
}

// apply runs the batch in a transaction, up to its commit. A command that fails on its own
// terms (datastale, the write rate) gets its error in results; any other error is a storage
// failure that aborts the whole batch, and the transaction is rolled back. On success the
// caller commits the returned transaction. Listens and their pushes are worked out command
// by command, in order, into state, so each listen sees exactly the writes after it.
func (d *Database) apply(ctx context.Context, batch []*command, state *batchState) (_ StoreTx, _ Commit, _ []Result, err error) {
	tx, err := d.store.Begin(ctx, d.info)
	if err != nil {
		return nil, Commit{}, nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	now := d.now()
	results := make([]Result, len(batch))
	var changes []Change
	// failed sorts a command's error: its own (*Error) goes in its result; any other is a
	// storage failure that aborts the batch.
	failed := func(i int, err error) bool {
		var e *Error
		if errors.As(err, &e) {
			results[i].Err = err
			return false
		}
		return err != nil
	}
	for i, c := range batch {
		state.sent = nil
		switch {
		case c.barrier:
			continue
		case c.listen != nil:
			if results[i], err = d.startListen(ctx, tx, c.acc, c.listen, state, now); err != nil {
				return nil, Commit{}, nil, err
			}
			continue
		case c.unlisten != nil:
			if l := d.listeners.remove(c.unlisten.sess, c.unlisten.key); l != nil {
				state.undo = append(state.undo, func() { d.listeners.add(l) })
			}
			continue
		case c.revoke != nil:
			if err = d.revokeListens(ctx, tx, c.revoke, state, now); err != nil {
				return nil, Commit{}, nil, err
			}
			continue
		case c.rules != nil:
			// The rules are saved already, so they stay even if the batch fails. Should the
			// check of the listens fail with the database, the listens stay too: while
			// nothing can be written, nothing new reaches them either.
			d.rules = c.rules
			if err = d.recheckListens(ctx, tx, state, now); err != nil {
				return nil, Commit{}, nil, err
			}
			continue
		case c.drop != nil:
			for _, l := range d.listeners.of(c.drop) {
				d.listeners.remove(l.sess, l.key())
				state.undo = append(state.undo, func() { d.listeners.add(l) })
			}
			continue
		case c.register != nil:
			if err = d.checkRegistration(ctx, tx, c.acc, c.register, now); failed(i, err) {
				return nil, Commit{}, nil, err
			}
			continue
		case c.write == nil:
			if results[i], err = d.read(ctx, tx, c, now); err != nil {
				return nil, Commit{}, nil, err
			}
			continue
		}
		ch, err := c.write.resolve(ctx, tx, now.UnixMilli())
		if failed(i, err) {
			return nil, Commit{}, nil, err
		} else if err != nil {
			continue
		}
		// The rules come before the write rate, so writes they refuse cost the database
		// nothing.
		if c.write.Separately {
			var left bool
			if ch, left, err = d.allowedPart(ctx, tx, c.acc, ch, now); err != nil {
				return nil, Commit{}, nil, err
			}
			if !left {
				continue
			}
		} else {
			rc, err := ruleChanges(ctx, tx, ch)
			if err != nil {
				return nil, Commit{}, nil, err
			}
			if err := d.mayWrite(ctx, tx, c.acc, now, rc...); failed(i, err) {
				return nil, Commit{}, nil, err
			} else if err != nil {
				continue
			}
		}
		if !d.rate.allow(now, c.write.Size) {
			results[i].Err = ErrWriteRate
			continue
		}
		if err := ch.store(ctx, tx); err != nil {
			return nil, Commit{}, nil, err
		}
		results[i].Node, results[i].Merge = ch.Value, ch.Merge
		changes = append(changes, ch)
		if err := d.fanout(ctx, tx, ch, state); err != nil {
			return nil, Commit{}, nil, err
		}
	}
	commit := Commit{Seq: d.info.Seq, Changes: changes}
	if len(changes) > 0 {
		if commit.Seq, err = tx.AdvanceSeq(ctx, int64(len(changes))); err != nil {
			return nil, Commit{}, nil, err
		}
	}
	return tx, commit, results, nil
}

// indexed reports whether the rules declare the index a query at p needs. Key and priority
// order need none.
func (d *Database) indexed(p Path, q Query) bool {
	key := q.IndexKey()
	if key == "" {
		return true
	}
	return d.rules != nil && slices.Contains(d.rules.IndexOn(p), key)
}

// read runs a get: the value at c.read, or the part of it a query selects. A query ordered by
// a child or by value needs its .indexOn, as in Firebase (ADR-020 §1), even with no range or
// limit: the emulator answers {"i":"s"} alone with no_index (golden query_get.json, r=12), and
// REST answers 400 with the same text. The rules come first.
func (d *Database) read(ctx context.Context, tx StoreTx, c *command, now time.Time) (Result, error) {
	a, p, q := c.acc, c.read, c.query
	rq := DefaultQuery.rules()
	switch {
	case q != nil:
		rq = q.rules()
	case c.rest:
		rq = rules.PlainREST
	}
	if err := d.mayRead(ctx, tx, a, p, rq, now); err != nil {
		var e *Error
		if errors.As(err, &e) {
			return Result{Err: err}, nil
		}
		return Result{}, err
	}
	if q != nil && !d.indexed(p, *q) {
		return Result{Err: NoIndexError(q.IndexKey(), p)}, nil
	}
	if c.probe {
		return Result{}, nil
	}
	n, err := tx.Read(ctx, p)
	if err != nil {
		return Result{}, err
	}
	if q != nil {
		n = q.Filter(n)
	}
	return Result{Node: n}, nil
}

// startListen registers a listen and sends its data first, unless the client's hash shows it
// already has them (B-emulator-oracle.md §1.4: no push for null, nor for a matching hash). A
// query the rules cannot index is served whole and untagged, with the no_index warning.
func (d *Database) startListen(ctx context.Context, tx StoreTx, a *access, c *listenCmd, state *batchState, now time.Time) (Result, error) {
	l := c.l
	if err := d.mayRead(ctx, tx, a, l.path, l.query.rules(), now); err != nil {
		var e *Error
		if errors.As(err, &e) {
			return Result{Err: err}, nil
		}
		return Result{}, err
	}
	n, err := tx.Read(ctx, l.path)
	if err != nil {
		return Result{}, err
	}
	var res Result
	view := n
	switch {
	case l.query.IsDefault():
		l.tag = 0
	case !d.indexed(l.path, l.query):
		l.tag = 0
		res.Warnings = []string{"no_index"}
	default:
		view = l.query.Filter(n)
		l.window = view
	}
	if Hash(view) != c.hash {
		state.deliver(l.sess, Push{Action: PushData, Path: l.path, Data: view.ExportJSON(), Tag: l.tag})
	}
	old := d.listeners.add(l)
	state.undo = append(state.undo, func() {
		d.listeners.remove(l.sess, l.key())
		if old != nil {
			d.listeners.add(old)
		}
	})
	return res, nil
}

// revokeListens checks the listens of a session against the rules again, as the session is
// now, and cancels those it may no longer read with a c push each: after unauth, a change of
// token, or a revoked one, as the emulator does (B-emulator-oracle.md §1.8, golden
// revoke_unauth.json and auth_change.json). The emulator's order of the pushes follows no rule
// a client could rely on; here it is by path, then by query.
func (d *Database) revokeListens(ctx context.Context, tx StoreTx, s *Session, state *batchState, now time.Time) error {
	ls := d.listeners.of(s)
	slices.SortFunc(ls, func(a, b *listen) int {
		if c := comparePaths(a.path, b.path); c != 0 {
			return c
		}
		return strings.Compare(a.key(), b.key())
	})
	a := s.access()
	for _, l := range ls {
		ok, err := d.readable(ctx, tx, a, l.path, l.query.rules(), now)
		if err != nil {
			return err
		}
		if ok {
			continue
		}
		d.listeners.remove(s, l.key())
		state.undo = append(state.undo, func() { d.listeners.add(l) })
		state.deliver(s, Push{Action: PushCancel, Path: l.path, Query: l.spec})
	}
	return nil
}

// recheckListens checks every listen against new rules, and cancels with a c push those they
// no longer permit. Firebase documents nothing on what a rules change does to listens that are
// open; the emulator 4.11.2 cancels them at once (golden rules_change.json), and so does
// Aruma, so that a rule tightened to stop a leak stops it now, not at the client's next
// reconnection. Listens the new rules still permit are left alone, with no new data.
func (d *Database) recheckListens(ctx context.Context, tx StoreTx, state *batchState, now time.Time) error {
	sessions := make([]*Session, 0, len(d.listeners.bySession))
	for s := range d.listeners.bySession {
		sessions = append(sessions, s)
	}
	for _, s := range sessions {
		if err := d.revokeListens(ctx, tx, s, state, now); err != nil {
			return err
		}
	}
	return nil
}

// checkRegistration checks an onDisconnect write as it is registered: a set as a write of its
// own, an update one child at a time, each against the stored data, as the emulator 4.11.2
// does (rules' matrix.golden.json). It writes nothing; the write runs, and is checked again,
// when the session closes (allowedPart).
func (d *Database) checkRegistration(ctx context.Context, tx StoreTx, a *access, w *Write, now time.Time) error {
	ch, err := w.resolve(ctx, tx, now.UnixMilli())
	if err != nil {
		return err
	}
	for _, part := range ch.parts() {
		rc, err := ruleChanges(ctx, tx, part)
		if err != nil {
			return err
		}
		if err := d.mayWrite(ctx, tx, a, now, rc...); err != nil {
			return err
		}
	}
	return nil
}

// allowedPart is the part of an onDisconnect write the rules let a session make as it
// closes: each registered write is checked on its own, against the data before any of them,
// and those denied are dropped while the others run (emulator 4.11.2, golden
// ondisconnect_rules.json). left is false when nothing is left.
func (d *Database) allowedPart(ctx context.Context, tx StoreTx, a *access, ch Change, now time.Time) (_ Change, left bool, err error) {
	if a == nil || a.auth.IsAdmin() {
		return ch, true, nil
	}
	rs := d.ruleset(a)
	if rs == nil {
		return Change{}, false, nil
	}
	var kept []Child
	for _, part := range ch.parts() {
		rc, err := ruleChanges(ctx, tx, part)
		if err != nil {
			return Change{}, false, err
		}
		ok, err := rs.CanWrite(ctx, env(a, tx, now), rc...)
		if err != nil {
			return Change{}, false, err
		}
		if !ok {
			d.log.Debug("rtdb: onDisconnect write denied when it ran", "database", d.name, "path", part.Path.String())
			continue
		}
		if ch.Merge == nil {
			return ch, true, nil
		}
		kept = append(kept, part.Merge[0])
	}
	if len(kept) == 0 {
		return Change{}, false, nil
	}
	return Change{Path: ch.Path, Merge: kept}, true, nil
}
