package documents

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anishathalye/porcupine"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// The run's knobs. The default run takes a few seconds and is part of `go test ./...` (and so of
// `make test`); -short leaves it out with every test that needs Postgres. `make linearizability`
// runs it longer, for a nightly job:
//
//	ARUMA_LINEARIZABILITY_DURATION  how long the clients run (a Go duration; default 3s), in
//	                                rounds of at most roundLength, each checked alone
//	ARUMA_LINEARIZABILITY_SEED      the random seed, to replay a run's choices (default: random)
const (
	groups           = 4 // independent groups of keysPerGroup documents: Porcupine's partitions
	clientsPerGroup  = 6
	defaultDuration  = 3 * time.Second
	roundLength      = 10 * time.Second
	cancelPercent    = 6 // operations run under a context that ends at a random moment
	recentReadTimes  = 32
	lockWaitTimeout  = 2 * time.Second
	minCheckTimeout  = 2 * time.Minute
	minimizeDeadline = 2 * time.Minute
)

var (
	admin   = core.Principal{Admin: true, ID: "linearizability@aruma.iam.gserviceaccount.com"}
	endUser = core.Principal{ID: "linearizability-user"}
	testDB  = core.DB{Project: 731_000_000_001, ProjectID: "linearizability", ID: core.DefaultDatabase}
)

func TestLinearizability(t *testing.T) {
	duration := defaultDuration
	if v := os.Getenv("ARUMA_LINEARIZABILITY_DURATION"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatalf("ARUMA_LINEARIZABILITY_DURATION: %v", err)
		}
		duration = d
	}
	seed := rand.Uint64()
	if v := os.Getenv("ARUMA_LINEARIZABILITY_SEED"); v != "" {
		s, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			t.Fatalf("ARUMA_LINEARIZABILITY_SEED: %v", err)
		}
		seed = s
	}
	t.Logf("seed %d, %v, %d groups of %d documents, %d clients each", seed, duration, groups, keysPerGroup, clientsPerGroup)

	pool := storetest.NewPool(t)
	store := core.NewPGStore(pool, core.Options{Transactions: core.TxnConfig{WaitTimeout: lockWaitTimeout}})
	if _, err := store.Database(t.Context(), testDB); err != nil {
		t.Fatal(err)
	}
	// A long run is rounds of at most roundLength, each on documents of its own and checked
	// alone: Porcupine's memory grows with the square of a group's history.
	for round, ran := 0, time.Duration(0); ran < duration; round++ {
		d := min(roundLength, duration-ran)
		if !runRound(t, store, round, seed, d) {
			break
		}
		ran += d
	}
}

// runRound runs the clients for d on the round's documents, then checks what they did. It
// reports whether every check passed.
func runRound(t *testing.T, store *core.PGStore, round int, seed uint64, d time.Duration) bool {
	h := &harness{t: t, s: store, start: time.Now()}
	h.deadline = h.start.Add(d)
	var wg sync.WaitGroup
	for g := range groups {
		for c := range clientsPerGroup {
			id := g*clientsPerGroup + c
			cl := &client{h: h, id: id, round: round, group: g, seen: map[int]int64{},
				rng: rand.New(rand.NewPCG(seed, uint64(round)<<32|uint64(id)))}
			wg.Go(cl.run)
		}
	}
	wg.Wait()
	h.finish()
	ops := h.ops
	t.Logf("round %d: %d operations recorded: %s; %+v", round, len(ops), h.counts(), store.TransactionStats())

	// Every transaction ended: none may hold a lock or wait for one (F2 review B1).
	if txns, locks := store.OpenLocks(testDB); txns != 0 || locks != 0 {
		t.Errorf("round %d: %d lock manager transactions and %d locks left", round, txns, locks)
	}
	checkCommitOrder(t, ops)
	checkRealTime(t, ops)
	checkUpdateTimes(t, ops)
	checkLinearizable(t, ops, max(minCheckTimeout, 4*d))
	return !t.Failed()
}

// harness records the operations of every client.
type harness struct {
	t        *testing.T
	s        *core.PGStore
	start    time.Time
	deadline time.Time

	mu      sync.Mutex
	ops     []porcupine.Operation
	recent  [groups][]stamp // read times worth reading at, per group
	pending []int           // ops of unknown outcome whose effect may come after they returned
	tally   map[string]int
	mixed   atomic.Int64 // read-only transactions whose reads answered at two read times
}

// stamp is a commit_time or read_time a client saw, and the call time of its operation: a read
// at that time linearizes after that call.
type stamp struct{ t, call int64 }

// now is the history's clock: nanoseconds since the start, monotonic.
func (h *harness) now() int64 { return time.Since(h.start).Nanoseconds() }

func (h *harness) record(client int, in *input, out *output, call, ret int64, lateEffect bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if lateEffect {
		h.pending = append(h.pending, len(h.ops))
	}
	h.ops = append(h.ops, porcupine.Operation{ClientId: client, Input: in, Output: out, Call: call, Return: ret})
	h.countLocked(string(in.Kind) + "/" + string(out.Outcome))
	if in.Mode != "" {
		h.countLocked(in.Mode + "/" + string(out.Outcome))
	}
	if out.Outcome != outOK {
		return
	}
	r := &h.recent[in.Group]
	for _, t := range []int64{out.CommitTime, out.ReadTime} {
		if t != 0 && in.Kind != opReadAt {
			*r = append(*r, stamp{t, call})
		}
	}
	if len(*r) > recentReadTimes {
		*r = slices.Delete(*r, 0, len(*r)-recentReadTimes)
	}
}

func (h *harness) count(what string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.countLocked(what)
}

func (h *harness) countLocked(what string) {
	if h.tally == nil {
		h.tally = map[string]int{}
	}
	h.tally[what]++
}

func (h *harness) counts() string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(h.tally)) {
		fmt.Fprintf(&b, "%s=%d ", k, h.tally[k])
	}
	return b.String()
}

// finish gives the operations whose effect may come after they returned (ErrUnknownOutcome) a
// return after every other operation.
func (h *harness) finish() {
	end := h.now() + 1
	for _, i := range h.pending {
		h.ops[i].Return = end
	}
}

// readTimeFor picks a time to read the group at: one a client saw, maybe a little later.
func (h *harness) readTimeFor(group int, rng *rand.Rand) (stamp, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.recent[group]
	if len(r) == 0 {
		return stamp{}, false
	}
	s := r[rng.IntN(len(r))]
	if rng.IntN(2) == 0 {
		s.t += rng.Int64N(300)
	}
	return s, true
}

// client is one client of one group, running operations until the deadline.
type client struct {
	h     *harness
	id    int
	round int
	group int
	rng   *rand.Rand
	seen  map[int]int64 // the last update_time seen of each key, for update_time preconditions
	count int64
}

func (c *client) run() {
	for time.Now().Before(c.h.deadline) {
		switch n := c.rng.IntN(100); {
		case n < 25:
			c.commit()
		case n < 40:
			c.read()
		case n < 50:
			c.readAt()
		case n < 68:
			c.transaction(admin, core.ConcurrencyDefault, "pessimistic")
		case n < 78:
			c.transaction(endUser, core.ConcurrencyOptimistic, "optimistic")
		case n < 84:
			c.transaction(admin, core.ConcurrencyOptimistic, "optimistic-admin")
		default:
			c.snapshot()
		}
		c.pause(2 * time.Millisecond)
	}
}

func (c *client) pause(upTo time.Duration) {
	if c.rng.IntN(3) > 0 {
		time.Sleep(time.Duration(c.rng.Int64N(int64(upTo))))
	}
}

// context returns the context of one operation: now and then one that ends at a random moment,
// as a client that gives up or disconnects.
func (c *client) context() (context.Context, context.CancelFunc, bool) {
	ctx := c.h.t.Context()
	if c.rng.IntN(100) < cancelPercent {
		ctx, cancel := context.WithTimeout(ctx, time.Duration(c.rng.Int64N(int64(4*time.Millisecond))))
		return ctx, cancel, true
	}
	return ctx, func() {}, false
}

func (c *client) path(key int) core.Path {
	return core.Path{"lin" + strconv.Itoa(c.round) + "-" + strconv.Itoa(c.group), "k" + strconv.Itoa(key)}
}

// unique returns a value for n that no other set writes.
func (c *client) unique() int64 {
	c.count++
	return int64(c.id+1)*1_000_000_000 + c.count*1000
}

func (c *client) keys(n int) []int {
	return c.rng.Perm(keysPerGroup)[:n]
}

// commit is Commit outside a transaction: one or two writes of distinct documents, each a set,
// an increment, a delete or a verify, with or without a precondition.
func (c *client) commit() {
	var writes []write
	for _, k := range c.keys(1 + c.rng.IntN(2)) {
		w := write{Key: k}
		switch n := c.rng.IntN(100); {
		case n < 40:
			w.Kind, w.N = wSet, c.unique()
		case n < 70:
			w.Kind, w.N = wIncr, 1+c.rng.Int64N(3)
		case n < 85:
			w.Kind = wDelete
		default:
			w.Kind = wVerify
		}
		if w.Kind == wVerify || c.rng.IntN(2) == 0 {
			w.Pre = c.precondition(k)
		}
		writes = append(writes, w)
	}
	in := &input{Kind: opCommit, Group: c.group, Writes: writes}
	ctx, cancel, _ := c.context()
	defer cancel()
	call := c.h.now()
	res, err := c.h.s.Commit(ctx, testDB, c.coreWrites(writes))
	ret := c.h.now()
	c.commitDone(ctx, in, nil, res, err, call, ret)
}

func (c *client) precondition(key int) precondition {
	switch n := c.rng.IntN(100); {
	case n < 30:
		return precondition{Exists: "exists"}
	case n < 50:
		return precondition{Exists: "missing"}
	case c.seen[key] != 0:
		return precondition{UpdateTime: c.seen[key]}
	}
	return precondition{Exists: "exists"}
}

func (c *client) coreWrites(writes []write) []core.Write {
	out := make([]core.Write, len(writes))
	for i, w := range writes {
		cw := core.Write{Path: c.path(w.Key)}
		switch w.Kind {
		case wSet:
			cw.Kind, cw.Fields = core.WriteUpdate, value.Map(value.Field{Name: "n", Value: value.Int(w.N)})
		case wIncr:
			cw.Kind, cw.Masked = core.WriteUpdate, true
			cw.Transforms = []core.Transform{{Path: value.FieldPath{"n"}, Kind: core.TransformIncrement, Operand: value.Int(w.N)}}
		case wDelete:
			cw.Kind = core.WriteDelete
		case wVerify:
			cw.Kind = core.WriteVerify
		}
		switch w.Pre.Exists {
		case "exists":
			cw.Precondition.Exists = new(true)
		case "missing":
			cw.Precondition.Exists = new(false)
		}
		if w.Pre.UpdateTime != 0 {
			cw.Precondition.UpdateTime = time.UnixMicro(w.Pre.UpdateTime)
		}
		out[i] = cw
	}
	return out
}

// commitDone records a commit's outcome: done, refused by a precondition, of unknown outcome, or
// left out when it surely did nothing (ABORTED).
func (c *client) commitDone(ctx context.Context, in *input, reads []observation, res core.CommitResult, err error, call, ret int64) {
	out := &output{Reads: reads}
	late := false
	e, isErr := errors.AsType[*core.Error](err)
	switch {
	case err == nil:
		out.Outcome, out.CommitTime, out.Seq = outOK, micros(res.CommitTime), res.Seq
		if len(res.Results) != len(in.Writes) {
			c.h.t.Errorf("%s: %d results for %d writes", in.Kind, len(res.Results), len(in.Writes))
			return
		}
		for i, r := range res.Results {
			out.UpdateTimes = append(out.UpdateTimes, micros(r.UpdateTime))
			var incr int64
			if in.Writes[i].Kind == wIncr {
				incr = r.TransformResults[0].IntValue()
			}
			out.Incr = append(out.Incr, incr)
			if in.Writes[i].Kind != wDelete && r.UpdateTime.After(time.Time{}) {
				c.seen[in.Writes[i].Key] = micros(r.UpdateTime)
			}
		}
	case errors.Is(err, core.ErrUnknownOutcome):
		out.Outcome, late = outUnknown, true
	case isErr && in.Kind == opCommit &&
		(e.Code == core.NotFound || e.Code == core.AlreadyExists || e.Code == core.FailedPrecondition):
		out.Outcome, out.Code = outRefused, int(e.Code)
	case isErr && e.Code == core.Aborted:
		c.h.count(string(in.Kind) + in.Mode + "/aborted")
		return
	case ctx.Err() != nil:
		// The client gave up during the commit: it may or may not have happened, before it
		// returned.
		out.Outcome = outUnknown
	default:
		c.h.t.Errorf("%s %s: unexpected error %v", in.Kind, describe(in, &output{Reads: reads}), err)
		return
	}
	c.h.record(c.id, in, out, call, ret, late)
}

func micros(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

// observe converts the documents a read returned.
func (c *client) observe(keys []int, docs []core.Document) []observation {
	out := make([]observation, len(keys))
	for i, d := range docs {
		o := observation{Key: keys[i], Exists: d.Exists()}
		if o.Exists {
			n, _ := d.Fields.Get("n")
			o.N, o.UT = n.IntValue(), micros(d.UpdateTime)
			c.seen[keys[i]] = o.UT
		}
		out[i] = o
	}
	return out
}

// get reads keys: Get for one (NOT_FOUND is a missing document, with no read time), BatchGet
// (which may ask a key twice) for more.
func (c *client) get(ctx context.Context, keys []int, o core.ReadOptions) ([]observation, int64, error) {
	if len(keys) == 1 {
		d, rt, err := c.h.s.Get(ctx, testDB, c.path(keys[0]), o)
		if e, ok := errors.AsType[*core.Error](err); ok && e.Code == core.NotFound {
			return []observation{{Key: keys[0]}}, 0, nil
		}
		if err != nil {
			return nil, 0, err
		}
		return c.observe(keys, []core.Document{d}), micros(rt), nil
	}
	paths := make([]core.Path, len(keys))
	for i, k := range keys {
		paths[i] = c.path(k)
	}
	docs, rt, err := c.h.s.BatchGet(ctx, testDB, paths, o)
	if err != nil {
		return nil, 0, err
	}
	return c.observe(keys, docs), micros(rt), nil
}

func (c *client) readKeys() []int {
	keys := c.keys(1 + c.rng.IntN(keysPerGroup))
	if len(keys) > 1 && c.rng.IntN(8) == 0 {
		keys = append(keys, keys[0]) // a path asked twice
	}
	return keys
}

// read is Get or BatchGet of the latest.
func (c *client) read() {
	keys := c.readKeys()
	ctx, cancel, _ := c.context()
	defer cancel()
	call := c.h.now()
	reads, rt, err := c.get(ctx, keys, core.ReadOptions{})
	ret := c.h.now()
	if err != nil {
		c.readFailed(ctx, opRead, err)
		return
	}
	c.h.record(c.id, &input{Kind: opRead, Group: c.group}, &output{Outcome: outOK, Reads: reads, ReadTime: rt}, call, ret, false)
}

func (c *client) readFailed(ctx context.Context, kind opKind, err error) {
	e, isErr := errors.AsType[*core.Error](err)
	switch {
	case ctx.Err() != nil:
		c.h.count(string(kind) + "/cancelled")
	case kind == opReadAt && errors.Is(err, core.ErrReadTimeFuture):
		c.h.count(string(kind) + "/future")
	case isErr && e.Code == core.Aborted:
		c.h.count(string(kind) + "/aborted")
	default:
		c.h.t.Errorf("%s: unexpected error %v", kind, err)
	}
}

// readAt reads at a read_time a client saw, or a little after it. It linearizes after the call
// of the operation that saw that time.
func (c *client) readAt() {
	at, ok := c.h.readTimeFor(c.group, c.rng)
	if !ok {
		return
	}
	keys := c.readKeys()
	ctx, cancel, _ := c.context()
	defer cancel()
	reads, rt, err := c.get(ctx, keys, core.ReadOptions{ReadTime: time.UnixMicro(at.t)})
	ret := c.h.now()
	if err != nil {
		c.readFailed(ctx, opReadAt, err)
		return
	}
	c.h.record(c.id, &input{Kind: opReadAt, Group: c.group, At: at.t}, &output{Outcome: outOK, Reads: reads, ReadTime: rt}, at.call, ret, false)
}

// transaction is a read-write transaction: it reads one or two documents, pauses, and writes
// what it read changed (an increment by a set, or a transfer between the two), deletes, writes
// a document it did not read, or writes nothing.
func (c *client) transaction(by core.Principal, mode core.Concurrency, name string) {
	ctx, cancel, _ := c.context()
	defer cancel()
	call := c.h.now()
	id, err := c.h.s.BeginTransaction(ctx, testDB, by, core.TransactionOptions{Concurrency: mode})
	if err != nil {
		c.readFailed(ctx, opTxn, err)
		return
	}
	ref := core.TransactionRef{ID: id, By: by}
	rollback := func() {
		// A context of its own: the transaction's may be over, and its locks must go.
		if err := c.h.s.Rollback(context.WithoutCancel(ctx), testDB, ref); err != nil {
			c.h.t.Errorf("rollback: %v", err)
		}
	}
	keys := c.keys(1 + c.rng.IntN(2))
	var reads []observation
	switch n := c.rng.IntN(3); {
	case len(keys) == 2 && n == 0:
		reads, _, err = c.get(ctx, keys, core.ReadOptions{Transaction: &ref})
	case len(keys) == 2 && n == 1:
		// Both reads at once, as Promise.all of two gets: the first to fail rolls the
		// transaction back while the other may still wait for its lock.
		reads, err = c.getParallel(ctx, keys, ref, rollback)
	default:
		for _, k := range keys {
			var r []observation
			if r, _, err = c.get(ctx, []int{k}, core.ReadOptions{Transaction: &ref}); err != nil {
				break
			}
			reads = append(reads, r...)
		}
	}
	if err != nil {
		rollback()
		c.readFailed(ctx, opTxn, err)
		return
	}
	c.pause(3 * time.Millisecond)

	var writes []write
	a := reads[0]
	switch n := c.rng.IntN(100); {
	case n < 45 || len(reads) == 1 && n < 80:
		writes = []write{{Key: a.Key, Kind: wSet, N: a.N + 1}}
	case n < 80:
		b := reads[1]
		writes = []write{{Key: a.Key, Kind: wSet, N: a.N - 1}, {Key: b.Key, Kind: wSet, N: b.N + 1}}
	case n < 88:
		writes = []write{{Key: a.Key, Kind: wDelete}}
	case n < 95:
		other := c.rng.IntN(keysPerGroup)
		if slices.Contains(keys, other) {
			writes = []write{{Key: a.Key, Kind: wSet, N: c.unique()}}
		} else {
			writes = []write{{Key: other, Kind: wSet, N: c.unique()}}
		}
	}
	in := &input{Kind: opTxn, Group: c.group, Mode: name, Writes: writes}
	if c.rng.IntN(20) == 0 {
		rollback()
		c.h.count(name + "/rolled back")
		return
	}
	res, err := c.h.s.CommitTransaction(ctx, testDB, ref, c.coreWrites(writes))
	ret := c.h.now()
	c.commitDone(ctx, in, reads, res, err, call, ret)
}

// getParallel reads each key in a goroutine of its own, in the transaction ref. The first read
// that fails runs onFail at once, with the others still running.
func (c *client) getParallel(ctx context.Context, keys []int, ref core.TransactionRef, onFail func()) ([]observation, error) {
	docs := make([]core.Document, len(keys))
	errs := make(chan error, len(keys))
	for i, k := range keys {
		go func() {
			d, _, err := c.h.s.Get(ctx, testDB, c.path(k), core.ReadOptions{Transaction: &ref})
			if e, ok := errors.AsType[*core.Error](err); ok && e.Code == core.NotFound {
				d, err = core.Document{Path: c.path(k)}, nil
			}
			docs[i] = d
			errs <- err
		}()
	}
	var first error
	for range keys {
		if err := <-errs; err != nil && first == nil {
			first = err
			onFail()
		}
	}
	if first != nil {
		return nil, first
	}
	return c.observe(keys, docs), nil
}

// snapshot is a read-only transaction: two reads, a pause apart, which must answer at one
// read_time. It linearizes within its first read, which fixes the snapshot, as production's
// does (f2-transactions.json read.run_query_read_only), not within its Begin: a pause between
// the two lets commits in, which the first read must see.
func (c *client) snapshot() {
	ctx, cancel, _ := c.context()
	defer cancel()
	by, who := admin, "admin"
	if c.rng.IntN(2) == 0 {
		by, who = endUser, "user"
	}
	id, err := c.h.s.BeginTransaction(ctx, testDB, by, core.TransactionOptions{ReadOnly: true})
	if err != nil {
		c.readFailed(ctx, opSnapshot, err)
		return
	}
	ref := core.TransactionRef{ID: id, By: by}
	defer func() {
		if err := c.h.s.Rollback(context.WithoutCancel(ctx), testDB, ref); err != nil {
			c.h.t.Errorf("rollback: %v", err)
		}
	}()
	c.pause(3 * time.Millisecond)
	var reads []observation
	var readTime, call, ret int64
	for i := range 2 {
		if i == 0 {
			call = c.h.now()
		} else {
			c.pause(3 * time.Millisecond)
		}
		r, rt, err := c.get(ctx, c.readKeys(), core.ReadOptions{Transaction: &ref})
		if i == 0 {
			ret = c.h.now()
		}
		if err != nil {
			c.readFailed(ctx, opSnapshot, err)
			return
		}
		if rt != 0 && readTime != 0 && rt != readTime {
			if n := c.h.mixed.Add(1); n <= 10 {
				c.h.t.Errorf("a read-only transaction read at %d, then at %d", readTime, rt)
			}
		}
		readTime = cmp.Or(readTime, rt)
		reads = append(reads, r...)
	}
	c.h.record(c.id, &input{Kind: opSnapshot, Group: c.group, Mode: "read-only-" + who}, &output{Outcome: outOK, Reads: reads, ReadTime: readTime}, call, ret, false)
}
