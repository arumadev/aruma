package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeProjects map[string]int64

func (f fakeProjects) ProjectNumber(_ context.Context, id string) (int64, error) {
	if n, ok := f[id]; ok {
		return n, nil
	}
	return 0, errors.New("not found")
}

const testNow = 1792507857299

func newTestService(t *testing.T, store Store, opts Options) *Service {
	t.Helper()
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.UnixMilli(testNow) }
	}
	s := New(store, fakeProjects{"chat-app": 482915736204, "other-app": 582915736204}, opts, discard)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Close(ctx)
	})
	return s
}

func open(t *testing.T, s *Service, ns string, auth Auth) *Session {
	t.Helper()
	target, err := s.Resolve(context.Background(), ns)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Open(target, auth)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Close)
	return sess
}

// sync helpers turn the callbacks into return values.
func set(s *Session, path, data string, hash *string) (*Node, error) {
	ch := make(chan Result, 1)
	s.Set(path, json.RawMessage(data), hash, func(n *Node, err error) { ch <- Result{n, err} })
	r := <-ch
	return r.Node, r.Err
}

func update(s *Session, path, data string) error {
	ch := make(chan error, 1)
	s.Update(path, json.RawMessage(data), func(err error) { ch <- err })
	return <-ch
}

func get(s *Session, path string) (string, error) {
	ch := make(chan Result, 1)
	s.Get(path, func(n *Node, err error) { ch <- Result{n, err} })
	r := <-ch
	return string(r.Node.ExportJSON()), r.Err
}

var admin = Auth{Admin: true}

func TestSessionReadsAndWrites(t *testing.T) {
	s := newTestService(t, newMemStore(), Options{})
	a := open(t, s, "chat-app-default-rtdb", admin)

	if _, err := set(a, "/x", `{"a":1,"b":"hi"}`, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := get(a, "/x"); got != `{"a":1,"b":"hi"}` {
		t.Errorf("get: %s", got)
	}
	if err := update(a, "/x", `{"b/c":20,"a":null,"e/f":5}`); err != nil {
		t.Fatal(err)
	}
	if got, _ := get(a, "x"); got != `{"b":{"c":20},"e":{"f":5}}` {
		t.Errorf("after update: %s", got)
	}
	if _, err := set(a, "/sv", `{"ts":{".sv":"timestamp"},"n":{".sv":{"increment":5}}}`, nil); err != nil {
		t.Fatal(err)
	}
	set(a, "/sv/n", `{".sv":{"increment":2.5}}`, nil)
	if got, _ := get(a, "/sv"); got != `{"n":7.5,"ts":1792507857299}` {
		t.Errorf("server values: %s", got)
	}

	// Conditional writes (golden/datastale.json).
	set(a, "/tx", `{"s":1}`, nil)
	wrong := "wronghash"
	if _, err := set(a, "/tx", `{"s":2}`, &wrong); !errors.Is(err, ErrDataStale) {
		t.Errorf("wrong hash: %v", err)
	}
	right := "8n5kEPKa77xiDo6xsHQrcom3mJ4="
	n, err := set(a, "/tx", `{"s":2}`, &right)
	if err != nil || string(n.ExportJSON()) != `{"s":2}` {
		t.Errorf("right hash: %v %v", n, err)
	}
	empty := ""
	if n, err := set(a, "/txmissing", `"new"`, &empty); err != nil || n.StringValue() != "new" {
		t.Errorf("empty hash on a missing path: %v %v", n, err)
	}

	// Validation happens before the writer.
	for _, c := range []struct {
		path, data string
		err        error
	}{
		{"/bad/x.y", `1`, ErrInvalidPath},
		{"/a", `{"a.b":1}`, ErrInvalidKey},
		{"/a", `{".sv":"unknown"}`, ErrInvalidServerValue},
		{strings.Repeat("/s", 33), `1`, ErrPathTooDeep},
	} {
		if _, err := set(a, c.path, c.data, nil); !errors.Is(err, c.err) {
			t.Errorf("set %.20s %s: %v, want %v", c.path, c.data, err, c.err)
		}
	}
	for _, c := range []struct {
		data string
		err  error
	}{
		{`{"a":1,"a/b":2}`, ErrOverlappingPaths},
		{`{"a":1,"/a":2}`, ErrOverlappingPaths},
		{`[1]`, ErrInvalidData},
		{`null`, ErrInvalidData},
		{`{"":1}`, ErrInvalidPath},
		{`{"a.b":1}`, ErrInvalidPath},
	} {
		if err := update(a, "/m", c.data); !errors.Is(err, c.err) {
			t.Errorf("update %s: %v, want %v", c.data, err, c.err)
		}
	}
	if err := update(a, "/m", `{}`); err != nil {
		t.Errorf("an empty update is fine: %v", err)
	}
}

func TestLockedRulesUntilR4(t *testing.T) {
	s := newTestService(t, newMemStore(), Options{})
	client := open(t, s, "chat-app-default-rtdb", Auth{})
	if _, err := set(client, "/a", `1`, nil); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("client write: %v", err)
	}
	if _, err := get(client, "/a"); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("client read: %v", err)
	}
	if err := client.OnDisconnectSet("/a", json.RawMessage(`1`)); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("client onDisconnect: %v", err)
	}
	// Signing in with an admin credential opens the data.
	client.SetAuth(admin)
	if _, err := set(client, "/a", `1`, nil); err != nil {
		t.Errorf("admin write: %v", err)
	}

	dev := newTestService(t, newMemStore(), Options{DevOpenRules: true})
	anyone := open(t, dev, "chat-app-default-rtdb", Auth{})
	if _, err := set(anyone, "/a", `1`, nil); err != nil {
		t.Errorf("dev open rules: %v", err)
	}
}

func TestResolve(t *testing.T) {
	s := newTestService(t, newMemStore(), Options{})
	if tg, err := s.Resolve(context.Background(), "chat-app-default-rtdb"); err != nil || tg.Project != 482915736204 || tg.ProjectID != "chat-app" {
		t.Errorf("resolve: %+v %v", tg, err)
	}
	for _, ns := range []string{"", "chat-app", "missing-default-rtdb", "-default-rtdb", "Chat-App-default-rtdb", "s-chat-default-rtdb", "chat-app-other"} {
		if _, err := s.Resolve(context.Background(), ns); !errors.Is(err, ErrNoDatabase) {
			t.Errorf("%q: %v", ns, err)
		}
	}
}

func TestNamespacesAreIsolated(t *testing.T) {
	s := newTestService(t, newMemStore(), Options{})
	a := open(t, s, "chat-app-default-rtdb", admin)
	b := open(t, s, "other-app-default-rtdb", admin)
	set(a, "/iso", `"A"`, nil)
	set(b, "/iso", `"B"`, nil)
	if got, _ := get(a, "/iso"); got != `"A"` {
		t.Errorf("a: %s", got)
	}
	if got, _ := get(b, "/iso"); got != `"B"` {
		t.Errorf("b: %s", got)
	}
}

func TestOnDisconnectRunsOnClose(t *testing.T) {
	store := newMemStore()
	var mu sync.Mutex
	var commits []Commit
	s := newTestService(t, store, Options{})
	s.OnCommit(func(_ *Database, c Commit) { mu.Lock(); commits = append(commits, c); mu.Unlock() })
	b := open(t, s, "chat-app-default-rtdb", admin)
	tg, _ := s.Resolve(context.Background(), "chat-app-default-rtdb")
	a, _ := s.Open(tg, admin)

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.OnDisconnectSet("/od/put", json.RawMessage(`"gone"`)))
	must(a.OnDisconnectUpdate("/od", json.RawMessage(`{"m1":1,"m2":{".sv":"timestamp"}}`)))
	must(a.OnDisconnectSet("/od/cancelme", json.RawMessage(`"x"`)))
	must(a.OnDisconnectCancel("/od/cancelme"))
	must(a.OnDisconnectSet("/od/inc", json.RawMessage(`{".sv":{"increment":1}}`)))
	if got, _ := get(b, "/od"); got != "null" {
		t.Fatalf("applied before close: %s", got)
	}
	a.Close()
	a.Close() // idempotent
	if got, _ := get(b, "/od"); got != `{"inc":1,"m1":1,"m2":1792507857299,"put":"gone"}` {
		t.Errorf("after close: %s", got)
	}
	// One commit, one merge at the common path, as the emulator pushes it.
	mu.Lock()
	defer mu.Unlock()
	if len(commits) != 1 || commits[0].Changes[0].Path.String() != "od" || len(commits[0].Changes[0].Merge) != 4 {
		t.Errorf("commits: %+v", commits)
	}
}

func TestWriterGroupsCommitsAndOrders(t *testing.T) {
	store := newMemStore()
	s := newTestService(t, store, Options{})
	var mu sync.Mutex
	var log []string
	s.OnCommit(func(_ *Database, c Commit) {
		mu.Lock()
		defer mu.Unlock()
		log = append(log, "commit")
		for _, ch := range c.Changes {
			log = append(log, "change "+ch.Path.String())
		}
	})
	a := open(t, s, "chat-app-default-rtdb", admin)

	// Hold the writer so the next writes queue up and share a transaction.
	block := make(chan struct{})
	a.db.submit(&command{read: Path{}, done: func(Result) { <-block }})
	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		a.Set("/w/"+string(rune('a'+i)), json.RawMessage(`1`), nil, func(_ *Node, err error) {
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			log = append(log, "ack "+string(rune('a'+i)))
			mu.Unlock()
			wg.Done()
		})
	}
	close(block)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if store.commits > 3 {
		t.Errorf("%d commits for %d queued writes; want them grouped", store.commits, n)
	}
	// Changes reach the hook before any ack of the same commit, in submission order.
	firstAck := -1
	for i, l := range log {
		if strings.HasPrefix(l, "ack") && firstAck < 0 {
			firstAck = i
		}
	}
	if firstAck < 0 || !strings.HasPrefix(log[firstAck-1], "change") {
		t.Errorf("acks before the changes: %v", log)
	}
	var acks []string
	for _, l := range log {
		if strings.HasPrefix(l, "ack") {
			acks = append(acks, l[4:])
		}
	}
	if strings.Join(acks, "") != "abcdefghijklmnopqrst" {
		t.Errorf("acks out of order: %v", acks)
	}
	if seq := store.dbs["chat-app-default-rtdb"].info.Seq; seq != n {
		t.Errorf("seq %d, want %d", seq, n)
	}
}

func TestWriterRetriesAFailedBatchOneByOne(t *testing.T) {
	store := newMemStore()
	s := newTestService(t, store, Options{})
	a := open(t, s, "chat-app-default-rtdb", admin)
	set(a, "/warm", `1`, nil) // open the database

	block := make(chan struct{})
	a.db.submit(&command{read: Path{}, done: func(Result) { <-block }})
	store.mu.Lock()
	store.failCommits = 1 // the batch fails; each retry succeeds
	store.mu.Unlock()
	errs := make(chan error, 3)
	for _, k := range []string{"a", "b", "c"} {
		a.Set("/r/"+k, json.RawMessage(`1`), nil, func(_ *Node, err error) { errs <- err })
	}
	close(block)
	for range 3 {
		if err := <-errs; err != nil {
			t.Errorf("write lost to a failed batch: %v", err)
		}
	}
	if got, _ := get(a, "/r"); got != `{"a":1,"b":1,"c":1}` {
		t.Errorf("after retry: %s", got)
	}

	// A single command whose commit fails is reported unavailable.
	store.mu.Lock()
	store.failCommits = 1
	store.mu.Unlock()
	if _, err := set(a, "/r/d", `1`, nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("failed commit: %v", err)
	}
}

func TestWriteRateLimit(t *testing.T) {
	now := time.UnixMilli(testNow)
	lim := DefaultLimits
	lim.WriteBytesPerMinute = 1 << 20
	s := newTestService(t, newMemStore(), Options{Limits: lim, Now: func() time.Time { return now }})
	a := open(t, s, "chat-app-default-rtdb", admin)
	big := `"` + strings.Repeat("x", 600<<10) + `"`
	if _, err := set(a, "/a", big, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := set(a, "/b", big, nil); !errors.Is(err, ErrWriteRate) {
		t.Errorf("second write in the same minute: %v", err)
	}
}

func TestRateWindow(t *testing.T) {
	start := time.UnixMilli(0)
	w := rateWindow{limit: 100, start: start}
	if !w.allow(start, 100) || w.allow(start, 1) {
		t.Fatal("limit within the first minute")
	}
	// Half a minute into the next window, half of the previous one still counts.
	at := start.Add(90 * time.Second)
	if !w.allow(at, 50) || w.allow(at, 1) {
		t.Error("sliding window")
	}
	if !w.allow(start.Add(5*time.Minute), 100) {
		t.Error("a quiet period resets the window")
	}
	if !(&rateWindow{}).allow(start, 1<<40) {
		t.Error("no limit")
	}
}

func TestConnectionLimit(t *testing.T) {
	lim := DefaultLimits
	lim.ConnectionsPerProject = 2
	s := newTestService(t, newMemStore(), Options{Limits: lim})
	tg, _ := s.Resolve(context.Background(), "chat-app-default-rtdb")
	other, _ := s.Resolve(context.Background(), "other-app-default-rtdb")
	r1, err1 := s.Connect(tg)
	_, err2 := s.Connect(tg)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if _, err := s.Connect(tg); !errors.Is(err, ErrTooManyConnections) {
		t.Errorf("third connection: %v", err)
	}
	if _, err := s.Connect(other); err != nil {
		t.Errorf("another project has its own limit: %v", err)
	}
	r1()
	if _, err := s.Connect(tg); err != nil {
		t.Errorf("after a release: %v", err)
	}
}

func TestIdleWriterRetires(t *testing.T) {
	s := newTestService(t, newMemStore(), Options{IdleTimeout: 10 * time.Millisecond})
	tg, _ := s.Resolve(context.Background(), "chat-app-default-rtdb")
	a, _ := s.Open(tg, admin)
	db := a.db
	set(a, "/x", `1`, nil)
	a.Close()
	select {
	case <-db.done:
	case <-time.After(2 * time.Second):
		t.Fatal("idle writer did not stop")
	}
	// The next session starts a new writer on the same data.
	b, _ := s.Open(tg, admin)
	defer b.Close()
	if b.db == db {
		t.Error("a retired writer was reused")
	}
	if got, _ := get(b, "/x"); got != "1" {
		t.Errorf("data after restart: %s", got)
	}
}
