package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"connectrpc.com/connect"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// outcome is what became of a write, once settled.
type outcome int

const (
	pending    outcome = iota
	acked              // the server said it applied it
	refused            // the server refused it before applying anything
	applied            // its reply was lost; read back after the restart, it was there
	notApplied         // its reply was lost; read back after the restart, it was not there
)

func (o outcome) present() bool { return o == acked || o == applied }

// marker is a document a write created: ops/<id> (atomic with the counters), tx/<id> and
// stx/<id> (made by a transaction), bw/<id> (one write of a BatchWrite).
type marker struct {
	coll    string
	id      string
	k       int64 // the amount the commit added to the counters (ops)
	created outcome
	deleted outcome // pending while no delete ran
}

// live reports whether the marker should exist at the end.
func (m *marker) live() bool { return m.created.present() && !m.deleted.present() }

// stamp is an acknowledged commit's commit_time with when it was sent and acknowledged, for
// the real-time order check.
type stamp struct {
	sent, acked time.Time
	commit      time.Time
	via         string
}

// client is one writer. It owns ops/c<i>-*, tx/c<i>-*, bw/c<i>-* and the counters totals/c<i>
// and ctr/t<i>; it shares totals/all (increments, which commute) and ctr/shared (transactions
// that contend).
type client struct {
	r    *run
	i    int
	rng  *rand.Rand
	apis []api
	n    int
	// admin clients send the emulators' owner token, as the Admin SDKs do, and their
	// transactions are pessimistic; the others are end users, whose transactions are optimistic.
	admin bool

	mu      sync.Mutex
	markers []*marker
	stamps  []stamp

	stats map[string]int
}

func (c *client) name(coll, id string) string { return c.r.db + "/documents/" + coll + "/" + id }

func (c *client) nextID() string {
	c.n++
	return fmt.Sprintf("c%d-%d", c.i, c.n)
}

func (c *client) pick() api { return c.apis[c.rng.IntN(len(c.apis))] }

func (c *client) count(k string) { c.stats[k]++ }

// loop runs operations until stop ends. Each operation runs to its end under the run's own
// context: a request cancelled by the client could still commit on the server after the client
// read its documents back, which would look like a write applied after it was settled.
func (c *client) loop(stop context.Context) {
	ctx := c.r.bg
	for stop.Err() == nil {
		if _, err := c.r.WaitUp(stop); err != nil {
			return
		}
		switch x := c.rng.IntN(100); {
		case x < 35:
			c.commitOps(ctx, 1+c.rng.IntN(3))
		case x < 40:
			c.commitOps(ctx, 20)
		case x < 52:
			c.deleteOp(ctx)
		case x < 67:
			c.txn(ctx, false)
		case x < 80:
			c.txn(ctx, true)
		default:
			c.batchWrite(ctx)
		}
	}
}

// settleCtx is the context reads after a lost reply use: they run to the end even after the
// clients were told to stop.
func (c *client) settleCtx() context.Context { return c.r.bg }

// readBack reads a document until a server answers, across restarts.
func (c *client) readBack(name string) *pb.Document {
	ctx := c.settleCtx()
	for {
		ep, err := c.r.WaitUp(ctx)
		if err != nil {
			return nil
		}
		d, err := c.apis[0].Get(ctx, name, nil)
		if err == nil {
			return d
		}
		if _, err := c.r.WaitNext(ctx, ep); err != nil {
			return nil
		}
	}
}

// afterLoss waits for the server after the one that lost a reply.
func (c *client) afterLoss(ep int64) {
	_, _ = c.r.WaitNext(c.settleCtx(), ep)
}

func (c *client) record(s stamp) {
	c.mu.Lock()
	c.stamps = append(c.stamps, s)
	c.mu.Unlock()
}

// commitOps creates n markers ops/<id> in one commit and adds their amounts to totals/all and
// totals/c<i> in the same commit: the markers and the counters change together or not at all.
func (c *client) commitOps(ctx context.Context, n int) {
	a := c.pick()
	var ms []*marker
	var writes []*pb.Write
	var sum int64
	for range n {
		m := &marker{coll: "ops", id: c.nextID(), k: 1 + c.rng.Int64N(9)}
		sum += m.k
		ms = append(ms, m)
		writes = append(writes, mustNotExist(update(c.name("ops", m.id), map[string]*pb.Value{"c": intV(int64(c.i)), "k": intV(m.k)})))
	}
	writes = append(writes, increment(c.name("totals", "all"), "sum", sum), increment(c.name("totals", fmt.Sprintf("c%d", c.i)), "sum", sum))
	c.mu.Lock()
	c.markers = append(c.markers, ms...)
	c.mu.Unlock()
	ep := c.r.Epoch()
	sent := time.Now()
	resp, err := a.Commit(ctx, &pb.CommitRequest{Database: c.r.db, Writes: writes})
	label := fmt.Sprintf("commit%d/%s", n, a.Name())
	switch {
	case err == nil:
		c.record(stamp{sent: sent, acked: time.Now(), commit: resp.GetCommitTime().AsTime(), via: label})
		if len(resp.GetWriteResults()) != len(writes) {
			c.r.Violation("%s %s: %d write results for %d writes", label, ms[0].id, len(resp.GetWriteResults()), len(writes))
		}
		for _, m := range ms {
			m.created = acked
		}
		c.count(label + " acked")
	case definite(err):
		for _, m := range ms {
			m.created = refused
		}
		c.count(fmt.Sprintf("%s refused %s", label, codeOf(err)))
		c.pause(err)
	default:
		c.count(label + " lost")
		c.afterLoss(ep)
		there := 0
		for _, m := range ms {
			if d := c.readBack(c.name("ops", m.id)); d != nil {
				m.created = applied
				there++
				if intOf(d, "k") != m.k {
					c.r.Violation("ops/%s holds k=%d, written k=%d", m.id, intOf(d, "k"), m.k)
				}
			} else {
				m.created = notApplied
			}
		}
		if there != 0 && there != len(ms) {
			c.r.Violation("%s %s..: %d of the commit's %d documents are there after a lost reply: the commit was split", label, ms[0].id, there, len(ms))
		}
		if there > 0 {
			c.count(label + " lost, applied")
		}
	}
}

// pause backs off after a refusal that a busy or restarting server gives.
func (c *client) pause(err error) {
	switch codeOf(err) {
	case connect.CodeResourceExhausted, connect.CodeAborted, connect.CodeUnavailable:
		time.Sleep(time.Duration(5+c.rng.IntN(20)) * time.Millisecond)
	}
}

// deleteOp deletes one of the client's live markers and takes its amount off the counters, in
// one commit with the precondition that the marker exists.
func (c *client) deleteOp(ctx context.Context) {
	c.mu.Lock()
	var live []*marker
	for _, m := range c.markers {
		if m.coll == "ops" && m.live() && m.deleted == pending {
			live = append(live, m)
		}
	}
	c.mu.Unlock()
	if len(live) == 0 {
		return
	}
	m := live[c.rng.IntN(len(live))]
	a := c.pick()
	writes := []*pb.Write{mustExist(del(c.name("ops", m.id))),
		increment(c.name("totals", "all"), "sum", -m.k), increment(c.name("totals", fmt.Sprintf("c%d", c.i)), "sum", -m.k)}
	ep := c.r.Epoch()
	sent := time.Now()
	resp, err := a.Commit(ctx, &pb.CommitRequest{Database: c.r.db, Writes: writes})
	label := "delete/" + a.Name()
	switch {
	case err == nil:
		c.record(stamp{sent: sent, acked: time.Now(), commit: resp.GetCommitTime().AsTime(), via: label})
		m.deleted = acked
		c.count(label + " acked")
	case definite(err):
		c.count(fmt.Sprintf("%s refused %s", label, codeOf(err)))
		if codeOf(err) == connect.CodeNotFound {
			c.r.Violation("delete of live ops/%s refused NOT_FOUND: the marker is gone", m.id)
		}
		c.pause(err)
	default:
		c.count(label + " lost")
		c.afterLoss(ep)
		if d := c.readBack(c.name("ops", m.id)); d == nil {
			m.deleted = applied
			c.count(label + " lost, applied")
		} else {
			m.deleted = notApplied
			m.deleted = pending // it may be deleted again
		}
	}
}

// txn is a read-modify-write transaction: it reads a counter, writes it plus one, and creates a
// marker that holds the new value. The client's own counter (ctr/t<i>) sees no contention;
// ctr/shared contends with every client. At the end the markers of a counter must hold 1..n
// exactly, n being the counter: an increment lost or applied twice shows as a gap or a repeat.
func (c *client) txn(ctx context.Context, shared bool) {
	a := c.pick()
	ctr, coll, field := c.name("ctr", fmt.Sprintf("t%d", c.i)), "tx", "t"
	if shared {
		ctr, coll, field = c.name("ctr", "shared"), "stx", "v"
	}
	label := fmt.Sprintf("%s/%s/%s", coll, a.Name(), map[bool]string{true: "pessimistic", false: "optimistic"}[c.admin])
	for attempt := 0; attempt < 8 && ctx.Err() == nil; attempt++ {
		ep := c.r.Epoch()
		mode := pb.TransactionOptions_OPTIMISTIC
		if c.admin {
			mode = pb.TransactionOptions_PESSIMISTIC
		}
		bt, err := a.BeginTransaction(ctx, &pb.BeginTransactionRequest{Database: c.r.db, Options: &pb.TransactionOptions{
			Mode: &pb.TransactionOptions_ReadWrite_{ReadWrite: &pb.TransactionOptions_ReadWrite{ConcurrencyMode: mode}}}})
		if err != nil {
			c.count(label + " begin failed")
			if !definite(err) {
				c.afterLoss(ep)
			}
			c.pause(err)
			continue
		}
		cur, err := a.Get(ctx, ctr, bt.GetTransaction())
		if err != nil {
			c.count(label + " read failed")
			_ = a.Rollback(c.settleCtx(), &pb.RollbackRequest{Database: c.r.db, Transaction: bt.GetTransaction()})
			if !definite(err) {
				c.afterLoss(ep)
			}
			c.pause(err)
			continue
		}
		next := intOf(cur, field) + 1
		m := &marker{coll: coll, id: c.nextID(), k: next}
		c.mu.Lock()
		c.markers = append(c.markers, m)
		c.mu.Unlock()
		writes := []*pb.Write{
			update(ctr, map[string]*pb.Value{field: intV(next)}),
			mustNotExist(update(c.name(coll, m.id), map[string]*pb.Value{"c": intV(int64(c.i)), field: intV(next)})),
		}
		sent := time.Now()
		resp, err := a.Commit(ctx, &pb.CommitRequest{Database: c.r.db, Writes: writes, Transaction: bt.GetTransaction()})
		switch {
		case err == nil:
			c.record(stamp{sent: sent, acked: time.Now(), commit: resp.GetCommitTime().AsTime(), via: label})
			m.created = acked
			c.count(label + " acked")
			return
		case definite(err):
			m.created = refused
			c.count(fmt.Sprintf("%s refused %s", label, codeOf(err)))
			c.pause(err)
			continue
		default:
			c.count(label + " lost")
			c.afterLoss(ep)
			d := c.readBack(c.name(coll, m.id))
			if d != nil {
				m.created = applied
				c.count(label + " lost, applied")
				if intOf(d, field) != next {
					c.r.Violation("%s/%s holds %d, written %d", coll, m.id, intOf(d, field), next)
				}
			} else {
				m.created = notApplied
			}
			return
		}
	}
}

// batchWrite sends a BatchWrite of creates: each write stands alone, with its own status.
func (c *client) batchWrite(ctx context.Context) {
	a := c.pick()
	n := 2 + c.rng.IntN(5)
	var ms []*marker
	var writes []*pb.Write
	for range n {
		m := &marker{coll: "bw", id: c.nextID()}
		ms = append(ms, m)
		writes = append(writes, mustNotExist(update(c.name("bw", m.id), map[string]*pb.Value{"c": intV(int64(c.i))})))
	}
	c.mu.Lock()
	c.markers = append(c.markers, ms...)
	c.mu.Unlock()
	label := "batchWrite/" + a.Name()
	ep := c.r.Epoch()
	resp, err := a.BatchWrite(ctx, &pb.BatchWriteRequest{Database: c.r.db, Writes: writes})
	switch {
	case err == nil:
		if len(resp.GetStatus()) != n {
			c.r.Violation("%s: %d statuses for %d writes", label, len(resp.GetStatus()), n)
		}
		for i, m := range ms {
			if i < len(resp.GetStatus()) && resp.GetStatus()[i].GetCode() == 0 {
				m.created = acked
			} else {
				m.created = refused
			}
		}
		c.count(label + " acked")
	case definite(err):
		for _, m := range ms {
			m.created = refused
		}
		c.count(fmt.Sprintf("%s refused %s", label, codeOf(err)))
		c.pause(err)
	default:
		c.count(label + " lost")
		c.afterLoss(ep)
		for _, m := range ms {
			if c.readBack(c.name("bw", m.id)) != nil {
				m.created = applied
			} else {
				m.created = notApplied
			}
		}
	}
}

var errStop = errors.New("stopped")
