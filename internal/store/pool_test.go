package store_test

import (
	"bytes"
	"context"
	"log/slog"
	"runtime"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/store"
	"github.com/arumadev/aruma/internal/store/storetest"
)

func TestDefaultMaxConns(t *testing.T) {
	want := int32(min(max(16, 4*runtime.GOMAXPROCS(0)), 64))
	if got := store.DefaultMaxConns(); got != want {
		t.Errorf("store.DefaultMaxConns() = %d, want %d", got, want)
	}
	for procs, want := range map[int]int32{1: 16, 4: 16, 8: 32, 16: 64, 64: 64} {
		old := runtime.GOMAXPROCS(procs)
		got := store.DefaultMaxConns()
		runtime.GOMAXPROCS(old)
		if got != want {
			t.Errorf("on %d CPUs: %d connections, want %d", procs, got, want)
		}
	}
}

func TestSetsMaxConns(t *testing.T) {
	for dsn, want := range map[string]bool{
		"postgres://u:p@h:5432/db":                                   false,
		"postgres://u:p@h:5432/db?sslmode=disable":                   false,
		"postgres://u:p@h:5432/db?sslmode=disable&pool_max_conns=32": true,
		"postgresql://h/db?pool_max_conns=4":                         true,
		"postgres://h/db?application_name=pool_max_conns":            false,
		"host=h dbname=db pool_max_conns=8":                          true,
		"pool_max_conns = 8 host=h":                                  true,
		"host=h dbname=db":                                           false,
		"host=h application_name=xpool_max_conns=1":                  false,
	} {
		if got := store.SetsMaxConnsForTest(dsn); got != want {
			t.Errorf("setsMaxConns(%q) = %v, want %v", dsn, got, want)
		}
	}
}

// Open sizes the pool with DefaultMaxConns unless the URL sets pool_max_conns, and CheckPoolSize
// logs the size and warns when Postgres accepts fewer connections.
func TestOpenPoolSize(t *testing.T) {
	ctx := context.Background()
	url := storetest.NewDB(t)
	check := func(dsn string) (int32, string) {
		t.Helper()
		pool, err := store.Open(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		var logs bytes.Buffer
		store.CheckPoolSize(ctx, pool, dsn, slog.New(slog.NewTextHandler(&logs, nil)))
		return pool.Config().MaxConns, logs.String()
	}
	size, logs := check(url)
	if size != store.DefaultMaxConns() || !strings.Contains(logs, "source=default") || strings.Contains(logs, "WARN") {
		t.Errorf("default: %d connections, logs %s", size, logs)
	}
	size, logs = check(url + "&pool_max_conns=3")
	if size != 3 || !strings.Contains(logs, "max_conns=3 source=pool_max_conns") || strings.Contains(logs, "WARN") {
		t.Errorf("pool_max_conns=3: %d connections, logs %s", size, logs)
	}
	// Opened lazily: a pool larger than Postgres allows only warns.
	size, logs = check(url + "&pool_max_conns=100000")
	if size != 100000 || !strings.Contains(logs, "level=WARN") || !strings.Contains(logs, "raise max_connections") {
		t.Errorf("pool_max_conns=100000: %d connections, logs %s", size, logs)
	}
}
