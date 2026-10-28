package app

import (
	"context"
	"testing"
	"time"

	documents "github.com/arumadev/aruma/internal/services/documents/core"
)

// TestDocumentsUsageLastFlush: the usage's last flush runs in Close, after the server has drained
// its requests, not when the run's context ends: what a request still draining counts after the
// signal is kept.
func TestDocumentsUsageLastFlush(t *testing.T) {
	a, pool, _ := lostApp(t)
	ctx, cancel := context.WithCancel(t.Context())
	if err := a.Documents.Boot(ctx, documents.InstanceOptions{}); err != nil {
		t.Fatal(err)
	}
	const project = 4242
	a.Documents.usage.Read(project, 1)
	cancel() // the signal: the server drains its requests
	time.Sleep(200 * time.Millisecond)
	a.Documents.usage.Read(project, 2) // a request that was draining
	a.Documents.Close()
	hs, err := documents.NewUsage(pool, nil, nil).Hours(t.Context(), project, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 1 || hs[0].Reads != 3 {
		t.Errorf("after Close: %+v, want 3 reads", hs)
	}
}
