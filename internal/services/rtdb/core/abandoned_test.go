package core

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// gateStore holds the writer in Begin until released, so commands queue behind it.
type gateStore struct {
	*memStore
	mu      sync.Mutex
	gate    chan struct{} // when non-nil, Begin waits for it to close
	entered chan struct{}
	begins  int
}

func (g *gateStore) Begin(ctx context.Context, db DatabaseInfo) (StoreTx, error) {
	g.mu.Lock()
	gate, entered := g.gate, g.entered
	g.begins++
	g.mu.Unlock()
	if gate != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
	}
	return g.memStore.Begin(ctx, db)
}

// A client that listens and disconnects while the writer is busy leaves its listen in the
// queue. The writer used to run it all the same, loading the whole node for a reader that was
// gone: a few dozen such clients of a large node kept the database from every other client for
// as long as it took to load the node once per listen (27 s for 30 listens to a 180,000-row node).
func TestWriterSkipsListensOfClosedSessions(t *testing.T) {
	gs := &gateStore{memStore: newMemStore()}
	s := newTestService(t, gs, Options{})
	writer := open(t, s, "chat-app-default-rtdb", admin)
	if _, err := set(writer, "/big", `{"a":1,"b":2}`, nil); err != nil {
		t.Fatal(err)
	}

	gone, rec := listenWith(t, s, "chat-app-default-rtdb", admin)
	reader, rec2 := listenWith(t, s, "chat-app-default-rtdb", admin)

	// Hold the writer inside a batch, and queue behind it: the leaving client's listen and get,
	// and a live client's listen.
	gs.mu.Lock()
	gs.gate, gs.entered = make(chan struct{}), make(chan struct{}, 1)
	gate, entered := gs.gate, gs.entered
	gs.mu.Unlock()
	held := make(chan struct{})
	writer.Set("/held", json.RawMessage(`1`), nil, func(*Node, error) { close(held) })
	<-entered

	var listenErr, getErr error
	var answered sync.WaitGroup
	answered.Add(2)
	gone.Listen("/big", nil, 0, "", func(_ []string, err error) { listenErr = err; answered.Done() })
	gone.Get("/big", nil, func(_ *Node, err error) { getErr = err; answered.Done() })
	liveDone := make(chan error, 1)
	reader.Listen("/big", nil, 0, "", func(_ []string, err error) { liveDone <- err })

	closed := make(chan struct{})
	go func() { gone.Close(); close(closed) }()
	for !gone.isClosed() {
		time.Sleep(time.Millisecond)
	}
	close(gate)
	<-closed
	answered.Wait()
	<-held
	if err := <-liveDone; err != nil {
		t.Fatalf("a live client's listen failed: %v", err)
	}

	if got := rec.take(); len(got) != 0 {
		t.Errorf("the writer loaded data for a closed session: %q", got)
	}
	if listenErr == nil || getErr == nil {
		t.Errorf("a closed session's read was answered as served: listen %v, get %v", listenErr, getErr)
	}
	want(t, "a live session's listen", rec2.take(), `d big {"a":1,"b":2}`)
}

// A write sent just before the client disconnects is still applied.
func TestWriterKeepsWritesOfClosedSessions(t *testing.T) {
	gs := &gateStore{memStore: newMemStore()}
	s := newTestService(t, gs, Options{})
	other := open(t, s, "chat-app-default-rtdb", admin)
	if _, err := set(other, "/x", `0`, nil); err != nil {
		t.Fatal(err)
	}
	leaving := open(t, s, "chat-app-default-rtdb", admin)

	gs.mu.Lock()
	gs.gate, gs.entered = make(chan struct{}), make(chan struct{}, 1)
	gate, entered := gs.gate, gs.entered
	gs.mu.Unlock()
	held := make(chan struct{})
	other.Set("/held", json.RawMessage(`1`), nil, func(*Node, error) { close(held) })
	<-entered

	wrote := make(chan error, 1)
	leaving.Set("/x", json.RawMessage(`7`), nil, func(_ *Node, err error) { wrote <- err })
	closed := make(chan struct{})
	go func() { leaving.Close(); close(closed) }()
	for !leaving.isClosed() {
		time.Sleep(time.Millisecond)
	}
	close(gate)
	<-closed
	<-held
	if err := <-wrote; err != nil {
		t.Fatalf("the write of a session that closed after sending it failed: %v", err)
	}
	if got, _ := get(other, "/x"); got != "7" {
		t.Errorf("/x = %s, want 7", got)
	}
}
