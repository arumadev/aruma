package app

import (
	"bytes"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// lockedBuffer is a log the server writes from several goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A server that stops by itself (here its listen fails) while ctx is alive still ends Storage's
// Run, so Close finds the gate free: Serve returns at once, the data directory's lock released,
// instead of waiting closeWait for a Run nobody told to stop.
func TestServeEndsStorageWhenTheServerFails(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	env := map[string]string{
		"ARUMA_DATABASE_URL":     storetest.NewDB(t),
		"ARUMA_PUBLIC_URL":       "http://127.0.0.1",
		"ARUMA_MASTER_KEY":       "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"ARUMA_LISTEN":           busy.Addr().String(),
		"ARUMA_METRICS_LISTEN":   "",
		"ARUMA_STORAGE_ENABLED":  "true",
		"ARUMA_STORAGE_DATA_DIR": t.TempDir(),
	}
	cfg, err := config.Load("", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	start := time.Now()
	err = Serve(t.Context(), cfg, slog.New(slog.NewTextHandler(logs, nil)), "test")
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("Serve = %v, want the listen failure", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("Serve took %v to return", took)
	}
	if strings.Contains(logs.String(), "storage: close") {
		t.Errorf("Storage did not close cleanly:\n%s", logs)
	}
}
