package listen

import (
	"maps"
	"math/rand/v2"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// TestMain makes a missing Postgres fail the integration tests rather than skip them; -short
// still leaves them out, as everywhere in the repository.
func TestMain(m *testing.M) {
	if os.Getenv("ARUMA_TEST_REQUIRE_DB") == "" {
		os.Setenv("ARUMA_TEST_REQUIRE_DB", "1")
	}
	os.Exit(m.Run())
}

var pgDB = core.DB{Project: 482915736204, ProjectID: "chatapp-8ae21", ID: core.DefaultDatabase}

func newPGHub(t *testing.T, o Options) (*Hub, *core.PGStore, *pgxpool.Pool) {
	t.Helper()
	if testing.Short() {
		t.Skip("Postgres")
	}
	pool := storetest.NewPool(t)
	h := NewHub(o)
	store := core.NewPGStore(pool, core.Options{Notifier: h})
	h.Bind(store)
	t.Cleanup(h.Close)
	if err := store.EnsureDatabase(t.Context(), pgDB.Project); err != nil {
		t.Fatal(err)
	}
	return h, store, pool
}

func fieldsA(a int64) value.Value { return value.Map(value.Field{Name: "a", Value: value.Int(a)}) }

// randomCommit writes one or two documents of c/d0..c/d19: a set with a in [0, 100), or a delete.
func randomCommit(r *rand.Rand) []core.Write {
	var ws []core.Write
	seen := map[int]bool{}
	for range 1 + r.IntN(2) {
		i := r.IntN(20)
		if seen[i] {
			continue
		}
		seen[i] = true
		p := core.ParsePath("c/d" + strconv.Itoa(i))
		if r.IntN(5) == 0 {
			ws = append(ws, core.Write{Kind: core.WriteDelete, Path: p})
		} else {
			ws = append(ws, core.Write{Kind: core.WriteUpdate, Path: p, Fields: fieldsA(int64(r.IntN(100)))})
		}
	}
	return ws
}

// view is a listener's copy of a target's result, kept from its snapshot and events.
type view map[string]int64

func (v view) apply(t *testing.T, c TargetChange) {
	t.Helper()
	k := c.After.Path.String()
	_, had := v[k]
	switch c.Kind {
	case Enter:
		if had {
			t.Errorf("%s entered twice", k)
		}
		v[k] = aOf(c.After)
	case Modify:
		if !had {
			t.Errorf("%s changed, not held", k)
		}
		v[k] = aOf(c.After)
	case LeaveExists, LeaveDeleted:
		if !had {
			t.Errorf("%s left, not held", k)
		}
		if (c.Kind == LeaveDeleted) == c.After.Exists() {
			t.Errorf("%s: %v with after %v", k, c.Kind, c.After.Exists())
		}
		delete(v, k)
	}
}

func aOf(d core.Document) int64 {
	a, _ := d.Fields.Get("a")
	return a.IntValue()
}

// current is the target's result now, read afresh.
func current(t *testing.T, store *core.PGStore, tg *Target) view {
	t.Helper()
	seq, rt, err := store.Head(t.Context(), pgDB)
	if err != nil {
		t.Fatal(err)
	}
	v := view{}
	if err := tg.ReadAt(t.Context(), Head{Seq: seq, ReadTime: rt}, func(d core.Document) error {
		v[d.Path.String()] = aOf(d)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return v
}

// listener subscribes while commits run, snapshots, and applies events until it has seen
// every commit up to the last one's seq, which done gives once the writers are over. It
// resyncs as asked.
func listener(t *testing.T, h *Hub, specs []Spec, done <-chan int64, slow bool) (map[*Target]view, *Subscriber) {
	ctx := t.Context()
	s, err := h.Subscribe(ctx, pgDB)
	if err != nil {
		t.Error(err)
		return nil, nil
	}
	views := map[*Target]view{}
	snaps := map[*Target]int64{}
	snapshot := func(tg *Target) {
		v := view{}
		head, err := tg.Snapshot(ctx, func(d core.Document) error {
			v[d.Path.String()] = aOf(d)
			return nil
		})
		if err != nil {
			t.Error(err)
		}
		views[tg], snaps[tg] = v, head.Seq
	}
	for _, spec := range specs {
		tg, err := s.Add(spec)
		if err != nil {
			t.Error(err)
			return nil, s
		}
		snapshot(tg)
	}
	last, end := int64(0), int64(-1)
	for {
		select {
		case <-s.Ready():
		case end = <-done:
			done = nil
			s.Await(end)
		case <-time.After(10 * time.Second):
			t.Errorf("listener stuck at %d, end %d", last, end)
			return views, s
		}
		if slow {
			time.Sleep(10 * time.Millisecond)
		}
		b := s.Take()
		if b.Resync {
			for tg := range views {
				snapshot(tg)
			}
			continue
		}
		for _, e := range b.Events {
			if e.Seq <= last {
				t.Errorf("event %d after %d", e.Seq, last)
			}
			last = e.Seq
			for _, c := range e.Changes {
				if e.Seq <= snaps[c.Target] {
					t.Errorf("event %d at or before its target's snapshot %d", e.Seq, snaps[c.Target])
				}
				views[c.Target].apply(t, c)
			}
		}
		if end >= 0 && b.Through.Seq >= end {
			return views, s
		}
	}
}

// Many writers commit while listeners subscribe and snapshot: each listener's view, from its
// snapshot and the events past it, ends equal to the target's result read afresh, with no
// event lost, doubled or out of order (ADR-024 §5).
// With a lag bound of 2 the slow listeners resync too, and end right all the same.
func TestListenersFollowConcurrentCommits(t *testing.T) {
	for _, lag := range []int{4096, 2} {
		t.Run(strconv.Itoa(lag), func(t *testing.T) { followConcurrentCommits(t, lag) })
	}
}

func followConcurrentCommits(t *testing.T, lag int) {
	h, store, _ := newPGHub(t, Options{LagEvents: lag})
	ctx := t.Context()
	filtered := `{"from":[{"collectionId":"c"}],"where":{"fieldFilter":{"field":{"fieldPath":"a"},"op":"GREATER_THAN_OR_EQUAL","value":{"integerValue":"50"}}}}`
	specs := func() []Spec {
		return []Spec{
			{ID: 1, Query: parseQuery(t, filtered, nil)},
			{ID: 2, Query: parseQuery(t, `{"from":[{"collectionId":"c","allDescendants":true}]}`, nil)},
			{ID: 3, Documents: []core.Path{core.ParsePath("c/d3"), core.ParsePath("c/d7")}},
		}
	}
	const writers, commits, listeners = 6, 40, 8
	var wg sync.WaitGroup
	for w := range writers {
		wg.Go(func() {
			r := rand.New(rand.NewPCG(uint64(w), 1))
			for range commits {
				if _, err := store.Commit(ctx, pgDB, randomCommit(r)); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	type result struct {
		views map[*Target]view
		sub   *Subscriber
	}
	results := make(chan result, listeners)
	dones := make([]chan int64, listeners)
	var lw sync.WaitGroup
	for i := range listeners {
		dones[i] = make(chan int64, 1)
		lw.Go(func() {
			time.Sleep(time.Duration(i) * 15 * time.Millisecond)
			v, s := listener(t, h, specs(), dones[i], i%4 == 3)
			results <- result{v, s}
		})
	}
	wg.Wait()
	end, _, err := store.Head(ctx, pgDB)
	if err != nil {
		t.Fatal(err)
	}
	if end < writers*commits/2 {
		t.Fatalf("head %d", end)
	}
	for _, d := range dones {
		d <- end
	}
	lw.Wait()
	close(results)
	for r := range results {
		if r.sub == nil {
			continue
		}
		for tg, v := range r.views {
			if want := current(t, store, tg); !maps.Equal(v, want) {
				t.Errorf("target %d: view %v, want %v", tg.ID, v, want)
			}
		}
		r.sub.Close()
	}
}

// A resumed target's changes since a commit, applied to its result then, give its result at
// the snapshot (ADR-026 §1.5), for a collection, a collection group and documents.
func TestChangesSinceResume(t *testing.T) {
	h, store, _ := newPGHub(t, Options{})
	ctx := t.Context()
	r := rand.New(rand.NewPCG(7, 7))
	for range 30 {
		if _, err := store.Commit(ctx, pgDB, randomCommit(r)); err != nil {
			t.Fatal(err)
		}
	}
	// Documents of other scopes, which the changes leave out.
	for _, p := range []string{"c/d1/c/x", "e/y", "c/d2/f/z"} {
		if _, err := store.Commit(ctx, pgDB, []core.Write{{Kind: core.WriteUpdate, Path: core.ParsePath(p), Fields: fieldsA(60)}}); err != nil {
			t.Fatal(err)
		}
	}
	s, err := h.Subscribe(ctx, pgDB)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	filtered := `{"from":[{"collectionId":"c"}],"where":{"fieldFilter":{"field":{"fieldPath":"a"},"op":"GREATER_THAN_OR_EQUAL","value":{"integerValue":"50"}}}}`
	for _, spec := range []Spec{
		{ID: 1, Query: parseQuery(t, filtered, nil)},
		{ID: 2, Query: parseQuery(t, `{"from":[{"collectionId":"c","allDescendants":true}]}`, nil)},
		{ID: 3, Documents: []core.Path{core.ParsePath("c/d3"), core.ParsePath("c/d7")}},
	} {
		tg, err := s.Add(spec)
		if err != nil {
			t.Fatal(err)
		}
		v := view{}
		since, err := tg.Snapshot(ctx, func(d core.Document) error {
			v[d.Path.String()] = aOf(d)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for range 30 {
			if _, err := store.Commit(ctx, pgDB, randomCommit(r)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.Commit(ctx, pgDB, []core.Write{{Kind: core.WriteUpdate, Path: core.ParsePath("c/d1/c/w"), Fields: fieldsA(70)}}); err != nil {
			t.Fatal(err)
		}
		seq, rt, err := store.Head(ctx, pgDB)
		if err != nil {
			t.Fatal(err)
		}
		upto := Head{Seq: seq, ReadTime: rt}
		for _, from := range []Head{since, {ReadTime: since.ReadTime}} { // with the seq, and the time alone
			got := maps.Clone(v)
			in, err := tg.Changes(ctx, from, upto, func(c TargetChange) error {
				got.apply(t, c)
				return nil
			})
			if err != nil || !in {
				t.Fatalf("target %d: in window %v, %v", spec.ID, in, err)
			}
			if want := current(t, store, tg); !maps.Equal(got, want) {
				t.Errorf("target %d from %+v: %v, want %v", spec.ID, from, got, want)
			}
		}
		// A time before the window is not.
		if in, err := tg.Changes(ctx, Head{ReadTime: time.Now().Add(-2 * time.Hour)}, upto, nil); err != nil || in {
			t.Errorf("target %d: an old time in window %v, %v", spec.ID, in, err)
		}
	}
}

// The hub reads the changelog for commits it was not told of: a store without a notifier
// commits, and the hub, told of a later commit alone, fills the rest from the changelog.
func TestGapFilledFromPostgres(t *testing.T) {
	h, store, pool := newPGHub(t, Options{})
	ctx := t.Context()
	s, err := h.Subscribe(ctx, pgDB)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Add(Spec{Query: parseQuery(t, `{"from":[{"collectionId":"c"}]}`, nil)}); err != nil {
		t.Fatal(err)
	}
	// A second store on the same pool, which tells no one.
	silent := core.NewPGStore(pool, core.Options{})
	var last core.CommitResult
	for i := range 5 {
		if last, err = silent.Commit(ctx, pgDB, []core.Write{{Kind: core.WriteUpdate, Path: core.ParsePath("c/g" + strconv.Itoa(i)), Fields: fieldsA(1)}}); err != nil {
			t.Fatal(err)
		}
	}
	res, err := store.Commit(ctx, pgDB, []core.Write{{Kind: core.WriteUpdate, Path: core.ParsePath("c/g9"), Fields: fieldsA(1)}})
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := collect(t, s, upTo(res.Seq))
	if len(evs) != 6 || evs[0].Seq != last.Seq-4 || evs[0].Changes[0].Kind != Enter {
		t.Fatalf("events %v", seqsOf(evs))
	}
}
