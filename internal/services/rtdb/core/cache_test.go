package core

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain checks the node cache against the store on every node it serves, and each window
// updated from a change against a full filter, in every test of the package (cache.go): a cache
// or a window that misses a change panics on the writer.
func TestMain(m *testing.M) {
	if os.Getenv("ARUMA_RTDB_CHECK_CACHE") == "" {
		os.Setenv("ARUMA_RTDB_CHECK_CACHE", "1")
	}
	os.Exit(m.Run())
}

const cacheNS = "chat-app-default-rtdb"

// cacheRules declare the index of the child queries below, so their listens are tagged.
const cacheRules = `{"rules": {".read": true, ".write": true, "q": {".indexOn": ["s"], "$k": {".indexOn": ["s"]}}}}`

func cacheService(t *testing.T, cacheRows int64) (*Service, *memStore) {
	t.Helper()
	store := newMemStore()
	store.rules = map[string]string{cacheNS: cacheRules}
	lim := DefaultLimits
	lim.CacheRows = cacheRows
	return newTestService(t, store, Options{Limits: lim}), store
}

// cacheQueries are tagged listens of every kind of window: by child, by key, by priority, by
// value, with limits from either end and ranges.
var cacheQueries = []struct{ path, q string }{
	{"/q", `{"i":"s","l":3,"vf":"r"}`},
	{"/q", `{"i":".key","l":2,"vf":"l"}`},
	{"/q", `{"i":".priority","l":4,"vf":"r"}`},
	{"/q", `{"i":"s","sp":2,"ep":7}`},
	{"/q", `{"i":".value","l":3,"vf":"l"}`},
	{"/q/a", `{"i":"s","l":2,"vf":"r"}`},
}

// randomOp writes something at a place that a cached node at /q or /q/a sees: below it, at it,
// above it, its priorities, updates of several children, deletes.
func randomOp(rng *rand.Rand, s *Session) {
	keys := []string{"a", "b", "c", "d", "e", "1", "10"}
	k := func() string { return keys[rng.IntN(len(keys))] }
	val := func() string {
		switch rng.IntN(7) {
		case 0:
			return "null"
		case 1:
			return fmt.Sprint(rng.IntN(10))
		case 2:
			return fmt.Sprintf(`"%s"`, k())
		case 3:
			return fmt.Sprintf(`{"s":%d,"t":%q}`, rng.IntN(10), k())
		case 4:
			return fmt.Sprintf(`{"s":%d,".priority":%d}`, rng.IntN(10), rng.IntN(5))
		case 5:
			return fmt.Sprintf(`{"%s":{"s":%d},"%s":{"s":%d}}`, k(), rng.IntN(10), k(), rng.IntN(10))
		}
		return "true"
	}
	switch rng.IntN(10) {
	case 0:
		set(s, "/q/"+k()+"/s", fmt.Sprint(rng.IntN(10)), nil)
	case 1:
		set(s, "/q/"+k(), val(), nil)
	case 2:
		set(s, "/q/"+k()+"/.priority", fmt.Sprint(rng.IntN(5)), nil)
	case 3:
		update(s, "/q", fmt.Sprintf(`{"%s/s":%d,"%s":%s}`, k(), rng.IntN(10), k(), val()))
	case 4:
		set(s, "/q/a/"+k(), val(), nil)
	case 5:
		set(s, "/q", fmt.Sprintf(`{"a":{"s":%d},"b":{"s":%d},"c":%s}`, rng.IntN(10), rng.IntN(10), val()), nil)
	case 6:
		set(s, "/", fmt.Sprintf(`{"q":{"%s":{"s":%d}},"other":1}`, k(), rng.IntN(10)), nil)
	case 7:
		update(s, "/", fmt.Sprintf(`{"q/%s/s":%d,"other":2}`, k(), rng.IntN(10)))
	case 8:
		set(s, "/q/.priority", fmt.Sprint(rng.IntN(5)), nil)
	default:
		set(s, "/other", val(), nil)
	}
}

// The same writes, with the cache and without it, give every query listen the same pushes;
// with the cache, a write reads the listened nodes again only when they leave it.
func TestCacheMatchesNoCache(t *testing.T) {
	t.Setenv("ARUMA_RTDB_CHECK_CACHE", "") // its reads would count below; the pushes check it here
	type side struct {
		writer *Session
		recs   []*recorder
		store  *memStore
	}
	start := func(cacheRows int64) side {
		svc, store := cacheService(t, cacheRows)
		sd := side{writer: open(t, svc, cacheNS, admin), store: store}
		set(sd.writer, "/q", `{"a":{"s":1,"x":{"s":3}},"b":{"s":5},"c":{"s":2,".priority":1}}`, nil)
		for i, c := range cacheQueries {
			sess, rec := listenWith(t, svc, cacheNS, admin)
			listenSync(t, sess, rec, c.path, c.q, int64(i+1), "")
			sd.recs = append(sd.recs, rec)
		}
		store.set(func() { store.reads = nil })
		return sd
	}
	cached, plain := start(DefaultLimits.CacheRows), start(0)
	rng1, rng2 := rand.New(rand.NewPCG(7, 7)), rand.New(rand.NewPCG(7, 7))
	for range 600 {
		randomOp(rng1, cached.writer)
		randomOp(rng2, plain.writer)
	}
	// A get waits for the writer, so every push is in.
	get(cached.writer, "/")
	get(plain.writer, "/")
	for i := range cacheQueries {
		got, want := cached.recs[i].take(), plain.recs[i].take()
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("listen %v:\n with the cache    %q\n without the cache %q", cacheQueries[i], got, want)
		}
		if len(want) < 10 {
			t.Errorf("listen %v got %d pushes; the writes should reach it more", cacheQueries[i], len(want))
		}
	}
	reads := func(st *memStore) int {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.reads["q"] + st.reads["q/a"]
	}
	if c, p := reads(cached.store), reads(plain.store); c*5 > p {
		t.Errorf("the listened nodes were read %d times with the cache, %d without", c, p)
	}
	if v, _ := get(cached.writer, "/"); v != func() string { s, _ := get(plain.writer, "/"); return s }() {
		t.Error("the two databases differ")
	}
}

// A batch that does not commit leaves nothing of itself in the cache: a commit that fails, one
// whose outcome is lost, a value the store refuses. The check of every node served (TestMain)
// fails the test should the cache keep a write the store did not. A commit whose outcome stays
// unknown may have taken effect: the listens' windows are filtered again in full.
func TestCacheAfterFailedCommits(t *testing.T) {
	svc, store := cacheService(t, DefaultLimits.CacheRows)
	w := open(t, svc, cacheNS, admin)
	l, rec := listenWith(t, svc, cacheNS, admin)
	set(w, "/q", `{"a":{"s":1},"b":{"s":2}}`, nil)
	listenSync(t, l, rec, "/q", `{"i":"s","l":1,"vf":"r"}`, 1, "")
	rec.take()

	store.set(func() { store.failCommits = 1 })
	if _, err := set(w, "/q/c", `{"s":9}`, nil); err == nil {
		t.Fatal("the failed commit succeeded")
	}
	set(w, "/q/a/s", `3`, nil)
	want(t, "after a failed commit", rec.take(), `d q/a t1 {"s":3}`)

	store.set(func() { store.lostAcks = 1 })
	set(w, "/q/d", `{"s":8}`, nil) // committed, whatever the reply said
	set(w, "/q/b/s", `4`, nil)
	want(t, "after a lost ack", rec.take(), `d q/d t1 {"s":8}`)

	store.set(func() { store.lostAcks, store.seqErrs = 1, 1 })
	if _, err := set(w, "/q/g", `{"s":20}`, nil); err == nil {
		t.Fatal("a commit of unknown outcome succeeded")
	}
	set(w, "/q/a/s", `5`, nil)
	want(t, "after a commit of unknown outcome", rec.take(), `d q/g t1 {"s":20}`)

	// An update whose second child the store refuses: its first child, applied to the cache
	// already, comes out of it again.
	store.set(func() { store.refuse = "bad" })
	if err := update(w, "/q", `{"e":{"s":99},"f":"bad"}`); err == nil {
		t.Fatal("the refused value was stored")
	}
	store.set(func() { store.refuse = "" })
	if v, _ := get(w, "/q"); strings.Contains(v, `"e"`) {
		t.Errorf("the refused update left its first child: %s", v)
	}
	set(w, "/q/e", `{"s":30}`, nil)
	want(t, "after a refused value", rec.take(), `d q/e t1 {"s":30}`)
}

// The cache keeps no more rows than its budget, across databases, and gives them back when the
// last query listen of a node goes and when its writer stops.
func TestCacheBudget(t *testing.T) {
	svc, _ := cacheService(t, 10)
	w := open(t, svc, cacheNS, admin)
	set(w, "/q", `{"a":{"s":1},"b":{"s":2},"c":{"s":3}}`, nil)
	set(w, "/big", `{"a":1,"b":2,"c":3,"d":4,"e":5,"f":6,"g":7,"h":8,"i":9,"j":10,"k":11}`, nil)
	used := func() int64 { get(w, "/"); return svc.registry.cache.used.Load() }

	l, rec := listenWith(t, svc, cacheNS, admin)
	listenSync(t, l, rec, "/q", `{"i":"s","l":1,"vf":"r"}`, 1, "")
	if u := used(); u != 3 {
		t.Errorf("one listened node of 3 rows: %d cached", u)
	}
	// A node larger than the budget is not cached, and its listen works as before.
	listenSync(t, l, rec, "/big", `{"i":".key","l":1,"vf":"r"}`, 2, "")
	set(w, "/big/z", `1`, nil)
	if u := used(); u != 3 {
		t.Errorf("after a node over the budget: %d cached", u)
	}
	// Another database shares the budget.
	other := open(t, svc, "other-app-default-rtdb", admin)
	set(other, "/q", `{"a":{"s":1},"b":{"s":2},"c":{"s":3},"d":{"s":4},"e":{"s":5},"f":{"s":6},"g":{"s":7},"h":{"s":8}}`, nil)
	l2, rec2 := listenWith(t, svc, "other-app-default-rtdb", admin)
	listenSync(t, l2, rec2, "/q", `{"i":".key","l":1,"vf":"r"}`, 1, "")
	get(other, "/")
	if u := used(); u > 10 {
		t.Errorf("two databases cache %d rows, over the budget of 10", u)
	}
	// The listens go: so do their nodes.
	l.Close()
	l2.Close()
	set(w, "/x", `1`, nil)
	set(other, "/x", `1`, nil)
	if u := used(); u != 0 {
		t.Errorf("no query listen left: %d rows cached", u)
	}
	_ = rec
}

// PurgeProject deletes a project's data outside its writers, which forget what they cached.
func TestCachePurge(t *testing.T) {
	svc, _ := cacheService(t, DefaultLimits.CacheRows)
	w := open(t, svc, cacheNS, admin)
	set(w, "/q", `{"a":{"s":1},"b":{"s":2}}`, nil)
	l, rec := listenWith(t, svc, cacheNS, admin)
	listenSync(t, l, rec, "/q", `{"i":"s","l":1,"vf":"r"}`, 1, "")
	if svc.registry.cache.used.Load() == 0 {
		t.Fatal("nothing cached")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := svc.PurgeProject(ctx, 482915736204); err != nil {
		t.Fatal(err)
	}
	if u := svc.registry.cache.used.Load(); u != 0 {
		t.Errorf("after the purge: %d rows cached", u)
	}
}

// A batch that does not commit gives back exactly the cache it found: the nodes it changed,
// the entries it added and the rows it charged.
func TestCacheRollback(t *testing.T) {
	budget := &cacheBudget{limit: 100}
	c := newNodeCache(budget)
	c.check = false // no store here to compare with
	committed := &batchState{}
	q := mustParse(t, `{"a":{"s":1},"b":{"s":2}}`)
	c.put(Path{"q"}, q, committed)
	before := budget.used.Load()

	state := &batchState{}
	c.apply(Path{"q", "a", "s"}, Int(5), state)
	c.apply(Path{"q", "c"}, mustParse(t, `{"s":3,"t":4}`), state)
	c.apply(Path{}, mustParse(t, `{"q":{"z":1}}`), state)
	c.put(Path{"r"}, mustParse(t, `{"x":1}`), state)
	if Equal(c.entries["q"].node, q) || c.entries["r"] == nil {
		t.Fatal("the batch changed nothing")
	}
	state.rollback()

	if got, ok, _ := c.get(context.Background(), nil, Path{"q"}); !ok || !Equal(got, q) {
		t.Errorf("after the rollback /q is %s, want %s", got.ExportJSON(), q.ExportJSON())
	}
	if _, ok, _ := c.get(context.Background(), nil, Path{"r"}); ok {
		t.Error("the entry the batch added stayed")
	}
	if u := budget.used.Load(); u != before {
		t.Errorf("after the rollback %d rows are charged, want %d", u, before)
	}
}
