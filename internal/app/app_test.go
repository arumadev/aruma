package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/rules"
)

var update = flag.Bool("update", false, "rewrite golden files")

// The console route table is part of the dashboard contract: services mount their routes into
// it, and moving a service's handlers must not add, drop, reorder or re-protect a route. A
// deliberate change is accepted with -update and reviewed in the diff of the golden file.
func TestConsoleRouteTable(t *testing.T) {
	cfg, err := config.Load("", func(k string) string {
		return map[string]string{
			"ARUMA_DATABASE_URL": "postgres://unused",
			"ARUMA_PUBLIC_URL":   "https://aruma.test",
			"ARUMA_MASTER_KEY":   "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	// Wiring opens no connection: the pool connects lazily and the test never sends a request.
	pool, err := pgxpool.New(context.Background(), "postgres://unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	a, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", pool)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, r := range a.Console.Routes() {
		access := "public"
		if !r.Public {
			var accepts []string
			for _, c := range []struct {
				ok   bool
				name string
			}{{r.Policy.Session, "session"}, {r.Policy.AccessToken, "token"}, {r.Policy.ServiceToken, "service"}} {
				if c.ok {
					accepts = append(accepts, c.name)
				}
			}
			access = strings.Join(accepts, "+")
		}
		fmt.Fprintf(&b, "%-6s %s %s\n", r.Method, r.Pattern, access)
	}
	got := b.String()

	path := filepath.Join("testdata", "console_routes.txt")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if got != string(want) {
		t.Errorf("console routes differ from %s:\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

// The rules engine gets the configured limits and one Inflight for the whole process.
func TestRulesWiring(t *testing.T) {
	cfg, err := config.Load("", func(k string) string {
		return map[string]string{
			"ARUMA_DATABASE_URL":         "postgres://unused",
			"ARUMA_PUBLIC_URL":           "https://aruma.test",
			"ARUMA_MASTER_KEY":           "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
			"ARUMA_RULES_CALL_DEPTH":     "10",
			"ARUMA_RULES_INFLIGHT_BYTES": "1048576",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(context.Background(), "postgres://unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	a, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", pool)
	if err != nil {
		t.Fatal(err)
	}
	want := rules.DefaultLimits()
	want.CallDepth, want.InflightBytes = 10, 1<<20
	if a.RulesLimits != want {
		t.Errorf("rules limits = %+v, want %+v", a.RulesLimits, want)
	}
	if a.RulesInflight == nil {
		t.Error("no Inflight for the rules engine")
	}
}
