package core

import (
	"context"
	"errors"
	"log/slog"
	"time"
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

	// Guarded by the registry's lock.
	refs int
	done chan struct{}
}

// Commit is what one transaction applied.
type Commit struct {
	// Seq is the database's write count after the commit; the changes are numbered
	// Seq-len(Changes)+1 to Seq.
	Seq     int64
	Changes []Change
}

// Result is a command's outcome. Node is the value read, or for a conditional write the value
// written: the emulator acks such a write with the data.
type Result struct {
	Node *Node
	Err  error
}

// command is one inbox entry: a write, or a read of Read when Write is nil.
type command struct {
	write *Write
	read  Path
	done  func(Result)
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
		inbox: make(chan *command, inboxSize),
		rate:  rateWindow{limit: limits.WriteBytesPerMinute, start: now()},
		done:  make(chan struct{}),
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

// execute applies a batch in one transaction. When the transaction fails, each command is
// retried on its own, so one bad command cannot fail the others.
func (d *Database) execute(batch []*command) {
	ctx, cancel := context.WithTimeout(context.Background(), batchTimeout)
	defer cancel()
	if !d.opened {
		info, err := d.store.OpenDatabase(ctx, d.project, d.name)
		if err != nil {
			d.log.Error("rtdb: open database", "database", d.name, "error", err)
			for _, c := range batch {
				c.done(Result{Err: ErrUnavailable})
			}
			return
		}
		d.info, d.opened = info, true
	}
	rate := d.rate
	commit, results, err := d.apply(ctx, batch)
	if err != nil {
		d.rate = rate
		if len(batch) > 1 {
			for _, c := range batch {
				d.execute([]*command{c})
			}
			return
		}
		d.log.Error("rtdb: write", "database", d.name, "error", err)
		batch[0].done(Result{Err: ErrUnavailable})
		return
	}
	if len(commit.Changes) > 0 {
		d.info.Seq = commit.Seq
		if d.onCommit != nil {
			d.onCommit(commit)
		}
	}
	for i, c := range batch {
		c.done(results[i])
	}
}

// apply runs the batch in a transaction and commits it. A command that fails on its own
// terms (datastale, the write rate) gets its error in results; any other error is a storage
// failure that aborts the whole batch.
func (d *Database) apply(ctx context.Context, batch []*command) (Commit, []Result, error) {
	tx, err := d.store.Begin(ctx, d.info)
	if err != nil {
		return Commit{}, nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	now := d.now()
	results := make([]Result, len(batch))
	var changes []Change
	for i, c := range batch {
		if c.write == nil {
			n, err := tx.Read(ctx, c.read)
			if err != nil {
				return Commit{}, nil, err
			}
			results[i].Node = n
			continue
		}
		if !d.rate.allow(now, c.write.Size) {
			results[i].Err = ErrWriteRate
			continue
		}
		ch, err := c.write.apply(ctx, tx, now.UnixMilli())
		var e *Error
		switch {
		case errors.As(err, &e):
			results[i].Err = err
			continue
		case err != nil:
			return Commit{}, nil, err
		}
		if c.write.Hash != nil {
			results[i].Node = ch.Value
		}
		changes = append(changes, ch)
	}
	commit := Commit{Seq: d.info.Seq, Changes: changes}
	if len(changes) > 0 {
		if commit.Seq, err = tx.AdvanceSeq(ctx, int64(len(changes))); err != nil {
			return Commit{}, nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Commit{}, nil, err
	}
	return commit, results, nil
}
