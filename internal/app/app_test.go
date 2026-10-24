package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/rules"
	storage "github.com/arumadev/aruma/internal/services/storage/core"
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
			"ARUMA_RULES_INFLIGHT_BYTES": "33554432",
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
	want.CallDepth, want.InflightBytes = 10, 32<<20
	if a.RulesLimits != want {
		t.Errorf("rules limits = %+v, want %+v", a.RulesLimits, want)
	}
	if a.RulesInflight == nil {
		t.Error("no Inflight for the rules engine")
	}
}

// config.StorageLimits is storage/core.Limits field for field (the conversion compiles), with the
// same defaults, and config refuses exactly the limits core.Limits.Validate refuses.
func TestStorageLimitsMirrorCore(t *testing.T) {
	if got := storage.Limits(config.Defaults().Storage.Limits); got != storage.DefaultLimits() {
		t.Errorf("config's Storage defaults = %+v, want core's %+v", got, storage.DefaultLimits())
	}
	for name, change := range map[string]func(*config.StorageLimits){
		"defaults":           func(l *config.StorageLimits) {},
		"no object":          func(l *config.StorageLimits) { l.ObjectBytes = 0 },
		"unlimited quota":    func(l *config.StorageLimits) { l.ProjectBytes, l.ObjectBytes = 0, 1<<40 },
		"object over quota":  func(l *config.StorageLimits) { l.ProjectBytes = 1 << 20 },
		"negative quota":     func(l *config.StorageLimits) { l.ProjectBytes = -1 },
		"empty over idle":    func(l *config.StorageLimits) { l.EmptySessionTTL = time.Hour },
		"idle over session":  func(l *config.StorageLimits) { l.UploadIdleTTL = 200 * time.Hour },
		"console over all":   func(l *config.StorageLimits) { l.ConsoleSessionTTL = 200 * time.Hour },
		"all anonymous":      func(l *config.StorageLimits) { l.AnonymousStagedFraction = 1 },
		"over all":           func(l *config.StorageLimits) { l.AnonymousStagedFraction = 1.01 },
		"pages over 1000":    func(l *config.StorageLimits) { l.ListMaxResults = 1001 },
		"negative header":    func(l *config.StorageLimits) { l.HeaderFieldBytes = -3 },
		"no starts":          func(l *config.StorageLimits) { l.UploadStartsPerSecond = 0 },
		"no minimum rate":    func(l *config.StorageLimits) { l.MinBytesPerSecond = 0 },
		"no session per uid": func(l *config.StorageLimits) { l.UploadSessionsPerUser = 0 },
	} {
		l := config.Defaults().Storage.Limits
		change(&l)
		env := map[string]string{
			"ARUMA_DATABASE_URL": "postgres://unused",
			"ARUMA_PUBLIC_URL":   "https://aruma.test",
			"ARUMA_MASTER_KEY":   "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		}
		v := reflect.ValueOf(l)
		for i, f := range reflect.VisibleFields(v.Type()) {
			key := "ARUMA_STORAGE_" + strings.ToUpper(f.Tag.Get("yaml"))
			switch x := v.Field(i).Interface().(type) {
			case time.Duration:
				env[key] = x.String()
			case float64:
				env[key] = strconv.FormatFloat(x, 'g', -1, 64)
			default:
				env[key] = fmt.Sprint(x)
			}
		}
		cfg, cfgErr := config.Load("", func(k string) string { return env[k] })
		coreErr := storage.Limits(l).Validate()
		if (cfgErr == nil) != (coreErr == nil) {
			t.Errorf("%s: config says %v, core says %v", name, cfgErr, coreErr)
		}
		if cfgErr == nil && storage.Limits(cfg.Storage.Limits) != storage.Limits(l) {
			t.Errorf("%s: config loaded %+v from %+v", name, cfg.Storage.Limits, l)
		}
	}
}

// With Storage enabled, app builds the service and mounts its APIs, which answer 501 until S3
// and S4; the dashboard's routes are those of the golden file until S5-B. Disabled, nothing is
// built and the paths fall through.
func TestStorageWiring(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	load := func(vars map[string]string) *config.Config {
		env := map[string]string{
			"ARUMA_DATABASE_URL": "postgres://unused",
			"ARUMA_PUBLIC_URL":   "https://aruma.test",
			"ARUMA_MASTER_KEY":   "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		}
		for k, v := range vars {
			env[k] = v
		}
		cfg, err := config.Load("", func(k string) string { return env[k] })
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	on, err := New(load(map[string]string{"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": t.TempDir()}),
		slog.New(slog.NewTextHandler(io.Discard, nil)), "test", pool)
	if err != nil {
		t.Fatal(err)
	}
	if on.Storage == nil || on.Users == nil {
		t.Fatal("Storage enabled, but no service or no Users")
	}
	if on.Storage.Limits() != storage.DefaultLimits() {
		t.Error("the service does not have the configured limits")
	}
	h := on.Server.Handler()
	for _, path := range []string{"/v0/b/chat-app.aruma.test/o", "/firebasestorage.googleapis.com/v0/b/x/o", "/storage/v1/b/x", "/upload/storage/v1/b/x/o"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s: %d %s, want the contract's 501", path, rec.Code, rec.Body)
		}
	}
	off, err := New(load(nil), slog.New(slog.NewTextHandler(io.Discard, nil)), "test", pool)
	if err != nil {
		t.Fatal(err)
	}
	if off.Storage != nil {
		t.Error("Storage disabled, but a service is built")
	}
	rec := httptest.NewRecorder()
	off.Server.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/storage/v1/b/x", nil))
	if rec.Code == http.StatusNotImplemented {
		t.Error("Storage disabled, but its API answers")
	}
	if len(on.Console.Routes()) != len(off.Console.Routes()) {
		t.Error("Storage adds console routes before S5-B")
	}
}
