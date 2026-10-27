package listen

import (
	"context"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/query"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

var testDB = core.DB{Project: 1, ProjectID: "p", ID: core.DefaultDatabase}

var t0 = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

func timeOf(seq int64) time.Time { return t0.Add(time.Duration(seq) * time.Millisecond) }

// fakeReader is a changelog and a set of documents in memory.
type fakeReader struct {
	mu    sync.Mutex
	head  int64
	log   map[int64]core.LoggedCommit
	floor int64 // the changelog lacks every seq below it
	docs  []core.Document
	fills int
}

func newFake() *fakeReader { return &fakeReader{log: map[int64]core.LoggedCommit{}} }

// commit logs a commit and makes it the head.
func (f *fakeReader) commit(c core.LoggedCommit) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log[c.Seq] = c
	f.head = max(f.head, c.Seq)
}

func (f *fakeReader) Head(context.Context, core.DB) (int64, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.head, timeOf(f.head), nil
}

func (f *fakeReader) LoggedCommits(_ context.Context, _ core.DB, after int64, limit int) (int64, time.Time, []core.LoggedCommit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fills++
	var out []core.LoggedCommit
	for s := after + 1; s <= min(f.head, after+int64(limit)); s++ {
		if c, ok := f.log[s]; ok && s >= f.floor {
			out = append(out, c)
		}
	}
	return f.head, timeOf(f.head), out, nil
}

func (f *fakeReader) Scan(_ context.Context, _ core.DB, r core.ScanRequest) (core.ListResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Document
	for _, d := range f.docs {
		if slices.Equal(d.Path.Parent(), r.Parent) && d.Path.CollectionID() == r.CollectionID {
			out = append(out, d)
		}
	}
	return core.ListResult{Documents: out}, nil
}

func (f *fakeReader) BatchGet(_ context.Context, _ core.DB, paths []core.Path, _ core.ReadOptions) ([]core.Document, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]core.Document, len(paths))
	for i, p := range paths {
		out[i] = core.Document{Path: p}
		for _, d := range f.docs {
			if slices.Equal(d.Path, p) {
				out[i] = d
			}
		}
	}
	return out, timeOf(f.head), nil
}

func (f *fakeReader) ChangesSince(context.Context, core.DB, core.ChangesRequest, func(before, after core.Document) error) (bool, error) {
	return false, nil
}

func parseQuery(t *testing.T, sq string, parent core.Path) *query.Query {
	t.Helper()
	m := &pb.StructuredQuery{}
	if err := protojson.Unmarshal([]byte(sq), m); err != nil {
		t.Fatal(err)
	}
	q, err := query.Parse(m, wire.Name{Database: wire.Database{Project: "p", ID: core.DefaultDatabase}, Path: parent})
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func doc(path string, a int64) core.Document {
	return core.Document{Path: core.ParsePath(path), Fields: value.Map(value.Field{Name: "a", Value: value.Int(a)}),
		CreateTime: t0, UpdateTime: t0.Add(time.Second)}
}

func gone(path string) core.Document { return core.Document{Path: core.ParsePath(path)} }

func change(before, after core.Document) core.Change {
	return core.Change{Path: cmpPath(before, after), Before: before, After: after}
}

func cmpPath(a, b core.Document) core.Path {
	if a.Path != nil {
		return a.Path
	}
	return b.Path
}

func logged(seq int64, changes ...core.Change) core.LoggedCommit {
	return core.LoggedCommit{Seq: seq, CommitTime: timeOf(seq), Changes: changes}
}

// newTestHub returns a hub on f with quick timings, closed at the test's end.
func newTestHub(t *testing.T, f *fakeReader, o Options) *Hub {
	t.Helper()
	o.GapTimeout = cmpOr(o.GapTimeout, 5*time.Millisecond)
	o.UnknownPoll = cmpOr(o.UnknownPoll, 5*time.Millisecond)
	o.UnknownFast = cmpOr(o.UnknownFast, 50*time.Millisecond)
	o.UnknownSlowPoll = cmpOr(o.UnknownSlowPoll, 20*time.Millisecond)
	h := NewHub(o)
	h.Bind(f)
	t.Cleanup(h.Close)
	return h
}

func cmpOr(a, b time.Duration) time.Duration {
	if a != 0 {
		return a
	}
	return b
}

// collect takes batches until done holds of the seqs of the events taken, or fails after 2 s.
func collect(t *testing.T, s *Subscriber, done func(seqs []int64, b Batch) bool) ([]Event, Batch) {
	t.Helper()
	var evs []Event
	var seqs []int64
	deadline := time.After(2 * time.Second)
	for {
		b := s.Take()
		evs = append(evs, b.Events...)
		for _, e := range b.Events {
			seqs = append(seqs, e.Seq)
		}
		if done(seqs, b) {
			return evs, b
		}
		select {
		case <-s.Ready():
		case <-deadline:
			t.Fatalf("timed out with events %v, last batch %+v", seqs, b)
		}
	}
}

func upTo(n int64) func([]int64, Batch) bool {
	return func(seqs []int64, _ Batch) bool { return len(seqs) > 0 && seqs[len(seqs)-1] >= n }
}

func seqsOf(evs []Event) []int64 {
	out := make([]int64, len(evs))
	for i, e := range evs {
		out[i] = e.Seq
	}
	return out
}

func subscribe(t *testing.T, h *Hub, spec Spec) (*Subscriber, *Target) {
	t.Helper()
	s, err := h.Subscribe(t.Context(), testDB)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	tg, err := s.Add(spec)
	if err != nil {
		t.Fatal(err)
	}
	return s, tg
}

func collectionC(t *testing.T) Spec {
	return Spec{ID: 1, Query: parseQuery(t, `{"from":[{"collectionId":"c"}]}`, nil)}
}

// commitN tells h of commit seq, writing c/d{seq}, and logs it in f.
func commitN(h *Hub, f *fakeReader, seq int64) {
	c := logged(seq, change(gone("c/d"+itoa(seq)), doc("c/d"+itoa(seq), seq)))
	f.commit(c)
	h.Committed(testDB, c.Seq, c.CommitTime, c.Changes)
}

func logN(f *fakeReader, seq int64) core.LoggedCommit {
	c := logged(seq, change(gone("c/d"+itoa(seq)), doc("c/d"+itoa(seq), seq)))
	f.commit(c)
	return c
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestReleaseInSeqOrder(t *testing.T) {
	f := newFake()
	h := newTestHub(t, f, Options{GapTimeout: time.Hour})
	s, _ := subscribe(t, h, collectionC(t))
	c1, c2, c3 := logN(f, 1), logN(f, 2), logN(f, 3)
	// Told out of order, and 2 twice: released 1, 2, 3, each once.
	h.Committed(testDB, 3, c3.CommitTime, c3.Changes)
	h.Committed(testDB, 2, c2.CommitTime, c2.Changes)
	if b := s.Take(); len(b.Events) != 0 {
		t.Fatalf("released ahead of seq 1: %v", seqsOf(b.Events))
	}
	h.Committed(testDB, 1, c1.CommitTime, c1.Changes)
	h.Committed(testDB, 2, c2.CommitTime, c2.Changes)
	evs, b := collect(t, s, upTo(3))
	if got := seqsOf(evs); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("events %v", got)
	}
	if b.Through.Seq != 3 || !b.Through.ReadTime.Equal(timeOf(3)) {
		t.Fatalf("through %+v", b.Through)
	}
}

func TestGapFilledFromChangelog(t *testing.T) {
	f := newFake()
	h := newTestHub(t, f, Options{})
	s, _ := subscribe(t, h, collectionC(t))
	logN(f, 1)
	logN(f, 2)
	commitN(h, f, 3) // 1 and 2 are never told of
	evs, _ := collect(t, s, upTo(3))
	if got := seqsOf(evs); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("events %v", got)
	}
}

func TestHoldLimitFillsAtOnce(t *testing.T) {
	f := newFake()
	h := newTestHub(t, f, Options{GapTimeout: 200 * time.Millisecond, HoldLimit: 2})
	s, _ := subscribe(t, h, collectionC(t))
	logN(f, 1)
	start := time.Now()
	commitN(h, f, 2)
	commitN(h, f, 3)
	collect(t, s, upTo(3))
	if d := time.Since(start); d > 150*time.Millisecond {
		t.Fatalf("filled after %v, the gap timeout", d)
	}
}

func TestUnknownSettledFromChangelog(t *testing.T) {
	f := newFake()
	h := newTestHub(t, f, Options{GapTimeout: time.Hour})
	s, _ := subscribe(t, h, collectionC(t))
	commitN(h, f, 1)
	logN(f, 2)
	h.Unknown(testDB, 2) // committed, though its writer lost the reply
	evs, b := collect(t, s, upTo(2))
	if got := seqsOf(evs); !slices.Equal(got, []int64{1, 2}) || b.Resync {
		t.Fatalf("events %v, batch %+v", got, b)
	}

	// The stamp's reply lost (seq 0): settled up to the head, then past it when it commits.
	logN(f, 3)
	h.Unknown(testDB, 0)
	collect(t, s, upTo(3))
	logN(f, 4) // the lost commit commits late, and nothing tells the hub
	evs, _ = collect(t, s, upTo(4))
	if got := seqsOf(evs); !slices.Equal(got, []int64{4}) {
		t.Fatalf("events %v", got)
	}
	d := h.lookup(testDB)
	d.mu.Lock()
	want, wantHead := d.want, d.wantHead
	d.mu.Unlock()
	if want != 0 || wantHead {
		t.Fatalf("still settling: want %d, wantHead %v", want, wantHead)
	}
}

func TestUnknownRolledBackSettlesWithTheNext(t *testing.T) {
	f := newFake()
	h := newTestHub(t, f, Options{GapTimeout: time.Hour})
	s, _ := subscribe(t, h, collectionC(t))
	h.Unknown(testDB, 1) // rolled back: seq 1 is taken again by the next commit
	time.Sleep(30 * time.Millisecond)
	commitN(h, f, 1)
	evs, _ := collect(t, s, upTo(1))
	if got := seqsOf(evs); !slices.Equal(got, []int64{1}) {
		t.Fatalf("events %v", got)
	}
}

func TestPrunedChangelogResyncs(t *testing.T) {
	f := newFake()
	h := newTestHub(t, f, Options{})
	s, _ := subscribe(t, h, collectionC(t))
	logN(f, 1)
	logN(f, 2)
	f.mu.Lock()
	f.floor = 2 // retention took seq 1 before the hub read it
	f.mu.Unlock()
	commitN(h, f, 3)
	_, b := collect(t, s, func(_ []int64, b Batch) bool { return b.Resync })
	if b.Through.Seq != 3 {
		t.Fatalf("resync at %+v, want the head", b.Through)
	}
	commitN(h, f, 4)
	evs, _ := collect(t, s, upTo(4))
	if got := seqsOf(evs); !slices.Equal(got, []int64{4}) {
		t.Fatalf("events after the resync %v", got)
	}
}

func TestLagOverflowResyncs(t *testing.T) {
	f := newFake()
	h := newTestHub(t, f, Options{LagEvents: 3})
	s, _ := subscribe(t, h, collectionC(t))
	for seq := int64(1); seq <= 5; seq++ {
		commitN(h, f, seq)
	}
	b := s.Take()
	if !b.Resync || len(b.Events) != 0 {
		t.Fatalf("batch %+v, want a resync", b)
	}
	commitN(h, f, 6)
	b = s.Take()
	if b.Resync || !slices.Equal(seqsOf(b.Events), []int64{6}) {
		t.Fatalf("after the resync: %+v", b)
	}
}

func TestWholeCommitPerEvent(t *testing.T) {
	f := newFake()
	h := newTestHub(t, f, Options{})
	s, t1 := subscribe(t, h, collectionC(t))
	t2, err := s.Add(Spec{ID: 2, Documents: []core.Path{core.ParsePath("c/x")}})
	if err != nil {
		t.Fatal(err)
	}
	c := logged(1, change(gone("c/x"), doc("c/x", 1)), change(gone("c/y"), doc("c/y", 1)), change(gone("e/z"), doc("e/z", 1)))
	f.commit(c)
	h.Committed(testDB, 1, c.CommitTime, c.Changes)
	evs, _ := collect(t, s, upTo(1))
	if len(evs) != 1 || len(evs[0].Changes) != 3 {
		t.Fatalf("events %+v", evs)
	}
	n := map[*Target]int{}
	for _, ch := range evs[0].Changes {
		n[ch.Target]++
	}
	if n[t1] != 2 || n[t2] != 1 {
		t.Fatalf("changes per target %v", n)
	}
}

func TestSnapshotSkipsWhatItHolds(t *testing.T) {
	f := newFake()
	h := newTestHub(t, f, Options{GapTimeout: time.Hour})
	s, tg := subscribe(t, h, collectionC(t))
	// Commits 1 and 2 are released before the snapshot; 3 committed but not yet told of.
	commitN(h, f, 1)
	commitN(h, f, 2)
	c3 := logN(f, 3)
	f.mu.Lock()
	f.docs = []core.Document{doc("c/d1", 1), doc("c/d2", 2), doc("c/d3", 3)}
	f.mu.Unlock()
	var got []string
	head, err := tg.Snapshot(t.Context(), func(d core.Document) error {
		got = append(got, d.Path.String())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if head.Seq != 3 || !slices.Equal(got, []string{"c/d1", "c/d2", "c/d3"}) {
		t.Fatalf("snapshot at %d: %v", head.Seq, got)
	}
	h.Committed(testDB, 3, c3.CommitTime, c3.Changes)
	commitN(h, f, 4)
	evs, _ := collect(t, s, upTo(4))
	if got := seqsOf(evs); !slices.Equal(got, []int64{4}) {
		t.Fatalf("events past the snapshot %v, want [4]", got)
	}
}

func TestSnapshotAwaitsItsSeq(t *testing.T) {
	f := newFake()
	h := newTestHub(t, f, Options{GapTimeout: time.Hour})
	s, tg := subscribe(t, h, Spec{ID: 1, Query: parseQuery(t, `{"from":[{"collectionId":"other"}]}`, nil)})
	c1 := logN(f, 1)
	head, err := tg.Snapshot(t.Context(), func(core.Document) error { return nil })
	if err != nil || head.Seq != 1 {
		t.Fatalf("snapshot %+v, %v", head, err)
	}
	if b := s.Take(); b.Through.Seq != 0 {
		t.Fatalf("through %d before seq 1 is released", b.Through.Seq)
	}
	h.Committed(testDB, 1, c1.CommitTime, c1.Changes)
	select {
	case <-s.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("no signal at the snapshot's seq")
	}
	if b := s.Take(); b.Through.Seq != 1 || len(b.Events) != 0 {
		t.Fatalf("batch %+v", b)
	}
}

func TestClassify(t *testing.T) {
	f := newFake()
	h := newTestHub(t, f, Options{})
	s, err := h.Subscribe(t.Context(), testDB)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	add := func(spec Spec) *Target {
		tg, err := s.Add(spec)
		if err != nil {
			t.Fatal(err)
		}
		return tg
	}
	eq1 := add(Spec{Query: parseQuery(t, `{"from":[{"collectionId":"c"}],"where":{"fieldFilter":{"field":{"fieldPath":"a"},"op":"EQUAL","value":{"integerValue":"1"}}}}`, nil)})
	single := add(Spec{Documents: []core.Path{core.ParsePath("c/x")}})
	group := add(Spec{Query: parseQuery(t, `{"from":[{"collectionId":"c","allDescendants":true}]}`, nil)})
	under := add(Spec{Parent: core.ParsePath("a/x"), Query: parseQuery(t, `{"from":[{"collectionId":"c","allDescendants":true}]}`, core.ParsePath("a/x"))})
	ordered := add(Spec{Query: parseQuery(t, `{"from":[{"collectionId":"c"}],"orderBy":[{"field":{"fieldPath":"b"}}]}`, nil)})
	cursor := add(Spec{Query: parseQuery(t, `{"from":[{"collectionId":"c"}],"orderBy":[{"field":{"fieldPath":"a"}}],"startAt":{"values":[{"integerValue":"2"}],"before":true}}`, nil)})

	for _, c := range []struct {
		name          string
		target        *Target
		before, after core.Document
		want          Kind
	}{
		{"filter enter", eq1, gone("c/x"), doc("c/x", 1), Enter},
		{"filter enter by update", eq1, doc("c/x", 2), doc("c/x", 1), Enter},
		{"filter change", eq1, doc("c/x", 1), doc("c/x", 1), Modify},
		{"filter leave exists", eq1, doc("c/x", 1), doc("c/x", 2), LeaveExists},
		{"filter leave deleted", eq1, doc("c/x", 1), gone("c/x"), LeaveDeleted},
		{"filter never", eq1, doc("c/x", 2), doc("c/x", 3), 0},
		{"filter other collection", eq1, gone("a/x/c/y"), doc("a/x/c/y", 1), 0},
		{"single create", single, gone("c/x"), doc("c/x", 5), Enter},
		{"single update", single, doc("c/x", 4), doc("c/x", 5), Modify},
		{"single delete", single, doc("c/x", 5), gone("c/x"), LeaveDeleted},
		{"single other", single, gone("c/y"), doc("c/y", 5), 0},
		{"group root", group, gone("c/y"), doc("c/y", 1), Enter},
		{"group nested", group, gone("a/x/c/y"), doc("a/x/c/y", 1), Enter},
		{"group other id", group, gone("c/y/d/z"), doc("c/y/d/z", 1), 0},
		{"group under parent", under, gone("a/x/b/y/c/z"), doc("a/x/b/y/c/z", 1), Enter},
		{"group outside parent", under, gone("b/y/c/z"), doc("b/y/c/z", 1), 0},
		{"group parent's sibling", under, doc("a/xx/c/z", 1), gone("a/xx/c/z"), 0},
		{"order field missing", ordered, gone("c/x"), doc("c/x", 1), 0},
		{"cursor before", cursor, gone("c/x"), doc("c/x", 1), 0},
		{"cursor leave", cursor, doc("c/x", 3), doc("c/x", 1), LeaveExists},
	} {
		if got := c.target.classify(c.before, c.after); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCloseLeaksNoGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()
	f := newFake()
	h := NewHub(Options{UnknownPoll: time.Millisecond})
	h.Bind(f)
	for range 3 {
		s, err := h.Subscribe(t.Context(), core.DB{Project: 1, ID: core.DefaultDatabase})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Add(collectionC(t)); err != nil {
			t.Fatal(err)
		}
	}
	h.Unknown(testDB, 9) // a filler that polls
	s, err := h.Subscribe(t.Context(), core.DB{Project: 2, ID: core.DefaultDatabase})
	if err != nil {
		t.Fatal(err)
	}
	s.Close() // leaving the last subscriber stops its database's filler
	h.Close()
	select {
	case <-s.Done():
	default:
		t.Fatal("subscriber not done")
	}
	if _, err := h.Subscribe(t.Context(), testDB); err != ErrClosed {
		t.Fatalf("subscribe after close: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines, %d before", runtime.NumGoroutine(), before)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
