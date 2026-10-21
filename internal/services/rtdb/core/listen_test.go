package core

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// recorder is a Sink that keeps a session's pushes, and the replies the test notes, in one
// sequence, so tests can check what came before what.
type recorder struct {
	mu  sync.Mutex
	log []string
}

func (r *recorder) Push(p Push) {
	r.add(describePush(p))
}

func describePush(p Push) string {
	if p.Action == PushAuthRevoked {
		return p.Action + " " + p.Status + " " + p.Reason
	}
	s := p.Action + " " + p.Path.String()
	if p.Tag != 0 {
		s += " t" + strconv.FormatInt(p.Tag, 10)
	}
	if p.Action == PushCancel {
		if p.Query != nil {
			s += " q"
		}
		return s
	}
	return s + " " + string(p.Data)
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, s)
}

// take returns what was recorded since the last take.
func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.log
	r.log = nil
	return out
}

func listenWith(t *testing.T, s *Service, ns string, auth Auth) (*Session, *recorder) {
	t.Helper()
	rec := &recorder{}
	tg, err := s.Resolve(context.Background(), ns)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Open(tg, auth, OpenOptions{Loopback: true, Sink: rec})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Close)
	return sess, rec
}

// listenSync starts a listen and notes its reply in the recorder.
func listenSync(t *testing.T, s *Session, rec *recorder, path, q string, tag int64, hash string) ([]string, error) {
	t.Helper()
	var sp *QuerySpec
	if q != "" {
		v := spec(t, q)
		sp = &v
	}
	type res struct {
		w   []string
		err error
	}
	ch := make(chan res, 1)
	s.Listen(path, sp, tag, hash, func(w []string, err error) {
		rec.add("ok listen " + path)
		ch <- res{w, err}
	})
	r := <-ch
	return r.w, r.err
}

func want(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("%s:\n got  %q\n want %q", what, got, want)
	}
}

func TestListenDefault(t *testing.T) {
	s := newTestService(t, newMemStore(), Options{})
	a, _ := listenWith(t, s, "chat-app-default-rtdb", admin)
	b, rec := listenWith(t, s, "chat-app-default-rtdb", admin)
	set(a, "/l", `{"a":1,"b":{"c":2}}`, nil)

	// The data come before the ok (golden/listen_basic.json); nothing for a matching hash,
	// nor for an empty location.
	listenSync(t, b, rec, "/l", "", 0, "")
	listenSync(t, b, rec, "/empty", "", 0, "")
	want(t, "listen", rec.take(), `d l {"a":1,"b":{"c":2}}`, "ok listen /l", "ok listen /empty")
	n, _ := get(a, "/l")
	listenSync(t, b, rec, "/l", "", 0, Hash(mustParse(t, n)))
	want(t, "listen with the client's hash", rec.take(), "ok listen /l")

	// Writes arrive as written: below, at, above, and merges (B-emulator-oracle.md §2).
	set(a, "/l/a", `5`, nil)
	set(a, "/l/b/c", `3`, nil)
	update(a, "/l", `{"x":1,"y/z":2,"a":null}`)
	set(a, "/", `{"l":{"k":1},"other":2}`, nil)
	set(a, "/other", `3`, nil)
	update(a, "/", `{"l/k":2,"other":4}`)
	set(a, "/l", `null`, nil)
	got := rec.take()
	// One write above two listens pushes to each; in which order is not defined (the
	// listeners' trie is a map), so the test fixes it.
	if len(got) > 4 && got[3] == `d empty null` {
		got[3], got[4] = got[4], got[3]
	}
	want(t, "pushes", got,
		`d l/a 5`,
		`d l/b/c 3`,
		`m l {"a":null,"x":1,"y/z":2}`,
		`d l {"k":1}`,
		`d empty null`,
		`d l/k 2`,
		`d l null`,
	)
}

func TestWritersListensBeforeItsAck(t *testing.T) {
	s := newTestService(t, newMemStore(), Options{})
	a, rec := listenWith(t, s, "chat-app-default-rtdb", admin)
	listenSync(t, a, rec, "/x", "", 0, "")
	done := make(chan struct{})
	a.Set("/x/y", json.RawMessage(`1`), nil, func(*Node, error) { rec.add("ack"); close(done) })
	<-done
	want(t, "order", rec.take(), "ok listen /x", "d x/y 1", "ack")
}

func TestListenTaggedWindow(t *testing.T) {
	s := newTestService(t, newMemStore(), Options{})
	a, _ := listenWith(t, s, "chat-app-default-rtdb", admin)
	b, rec := listenWith(t, s, "chat-app-default-rtdb", admin)
	set(a, "/q", `{"a":1,"b":2,"c":3,"d":4}`, nil)

	// limitToLast(2) in priority (then key) order: filtered and tagged, no index needed.
	if w, err := listenSync(t, b, rec, "/q", `{"l":2,"vf":"r"}`, 1, ""); err != nil || w != nil {
		t.Fatal(w, err)
	}
	want(t, "initial window", rec.take(), `d q t1 {"c":3,"d":4}`, "ok listen /q")

	set(a, "/q/e", `5`, nil)    // enters; d is pushed out by the limit, silently
	set(a, "/q/a", `9`, nil)    // outside the window: nothing
	set(a, "/q/e", `6`, nil)    // changes in the window
	set(a, "/q/e", `null`, nil) // leaves, and c comes in
	set(a, "/q/d", `{"deep":1}`, nil)
	want(t, "window changes", rec.take(),
		`d q/e t1 5`,
		`d q/e t1 6`,
		`m q t1 {"c":3,"e":null}`,
		`d q/d t1 {"deep":1}`,
	)
	// A write above the listen replaces the window.
	set(a, "/", `{"q":{"z":1}}`, nil)
	want(t, "replaced", rec.take(), `d q t1 {"z":1}`)

	// After unlisten nothing comes.
	done := make(chan error, 1)
	q := spec(t, `{"l":2,"vf":"r"}`)
	b.Unlisten("/q", &q, 1, func(err error) { done <- err })
	<-done
	set(a, "/q/y", `1`, nil)
	want(t, "after unlisten", rec.take())
}

func TestListenChildOrderNeedsIndex(t *testing.T) {
	store := newMemStore()
	store.rules = map[string]string{"chat-app-default-rtdb": `{"rules":{".read":false,".write":false,"q":{".indexOn":["s"]}}}`}
	s := newTestService(t, store, Options{})
	a, _ := listenWith(t, s, "chat-app-default-rtdb", admin)
	b, rec := listenWith(t, s, "chat-app-default-rtdb", admin)
	set(a, "/q", `{"a":{"s":3},"b":{"s":1},"c":{"s":2},"d":{"s":0}}`, nil)
	set(a, "/r", `{"a":{"s":3},"b":{"s":1}}`, nil)

	// Indexed: filtered and tagged (golden/query addendum).
	w, err := listenSync(t, b, rec, "/q", `{"i":"s","l":2,"vf":"l"}`, 1, "")
	if err != nil || w != nil {
		t.Fatal(w, err)
	}
	// Not indexed: everything, untagged, with the warning.
	if w, err := listenSync(t, b, rec, "/r", `{"i":"s","l":1,"vf":"l"}`, 2, ""); err != nil || len(w) != 1 || w[0] != "no_index" {
		t.Fatalf("unindexed listen: %v %v", w, err)
	}
	want(t, "initial", rec.take(),
		`d q t1 {"b":{"s":1},"d":{"s":0}}`, "ok listen /q",
		`d r {"a":{"s":3},"b":{"s":1}}`, "ok listen /r")

	set(a, "/q/e", `{"s":-1}`, nil) // enters first; b goes out silently
	set(a, "/q/a/s", `-5`, nil)     // moves in, the whole child; d goes out silently
	set(a, "/q/e/s", `9`, nil)      // moves out with its new value; d comes back
	set(a, "/r/c", `{"s":0}`, nil)  // untagged: as written
	want(t, "changes", rec.take(),
		`d q/e t1 {"s":-1}`,
		`d q/a t1 {"s":-5}`,
		`m q t1 {"d":{"s":0},"e":{"s":9}}`,
		`d r/c {"s":0}`,
	)

	// get: the same query fails without the index, works with it.
	g := func(path, q string) (string, error) {
		sp := spec(t, q)
		ch := make(chan Result, 1)
		b.Get(path, &sp, func(n *Node, err error) { ch <- Result{Node: n, Err: err} })
		r := <-ch
		return string(r.Node.ExportJSON()), r.Err
	}
	if got, err := g("/q", `{"i":"s","l":1,"vf":"l"}`); err != nil || got != `{"a":{"s":-5}}` {
		t.Errorf("indexed get: %s %v", got, err)
	}
	_, err = g("/r", `{"i":"s","l":1,"vf":"l"}`)
	var e *Error
	if !errors.As(err, &e) || e.Status != "no_index" || e.Message != `Index not defined, add ".indexOn": "s", for path "/r", to the rules` {
		t.Errorf("unindexed get: %v", err)
	}
	if _, err := g("/r", `{"i":"s"}`); !errors.As(err, &e) || e.Status != "no_index" {
		t.Errorf("unindexed get without a limit: %v", err)
	}
	if got, err := g("/r", `{"i":".key","l":1,"vf":"r"}`); err != nil || got != `{"c":{"s":0}}` {
		t.Errorf("key get: %s %v", got, err)
	}
}

func TestRevokeAndClose(t *testing.T) {
	s := newTestService(t, newMemStore(), Options{})
	a, _ := listenWith(t, s, "chat-app-default-rtdb", admin)
	b, rec := listenWith(t, s, "chat-app-default-rtdb", admin)
	listenSync(t, b, rec, "/x", "", 0, "")
	listenSync(t, b, rec, "/y", `{"l":1,"vf":"l"}`, 3, "")
	rec.take()

	// Signed out, under locked rules, every listen is cancelled with c, before the reply.
	b.SetAuth(Auth{})
	done := make(chan struct{})
	b.RevokeListens(func() { rec.add("unauth ok"); close(done) })
	<-done
	got := rec.take()
	if len(got) != 3 || got[2] != "unauth ok" || !(got[0] == "c x" && got[1] == "c y q" || got[0] == "c y q" && got[1] == "c x") {
		t.Errorf("revoke: %q", got)
	}
	set(a, "/x", `1`, nil)
	want(t, "after revoke", rec.take())

	// A closing session's listens go before its onDisconnect writes run.
	c, recC := listenWith(t, s, "chat-app-default-rtdb", admin)
	listenSync(t, c, recC, "/od", "", 0, "")
	if err := onDisconnectSet(c, "/od/me", `"gone"`); err != nil {
		t.Fatal(err)
	}
	recC.take()
	c.Close()
	want(t, "own onDisconnect", recC.take())
}

// A batch that does not commit leaves the listens and their windows as they were.
func TestListenStateRollsBack(t *testing.T) {
	store := newMemStore()
	s := newTestService(t, store, Options{})
	a, _ := listenWith(t, s, "chat-app-default-rtdb", admin)
	b, rec := listenWith(t, s, "chat-app-default-rtdb", admin)
	set(a, "/q", `{"a":1,"b":2}`, nil)
	listenSync(t, b, rec, "/q", `{"l":1,"vf":"r"}`, 1, "")
	rec.take()

	errs := queue(t, a, map[string]string{"c": `3`}, func() { store.set(func() { store.failCommits = 1 }) })
	if !errors.Is(errs["c"], ErrUnavailable) {
		t.Fatal(errs)
	}
	want(t, "nothing from a failed batch", rec.take())
	// The window is still {b}: c entering now is pushed again.
	set(a, "/q/c", `3`, nil)
	want(t, "after the failed batch", rec.take(), `d q/c t1 3`)
}
