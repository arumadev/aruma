package app

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/config"
	authapi "github.com/arumadev/aruma/internal/services/auth/api"
)

// config's Auth rate limits default to the Auth API's own.
func TestAuthLimitsMirrorAPI(t *testing.T) {
	c, l := config.Defaults().Auth.Limits, authapi.ClientLimits
	if c.RequestsPerMinute != l.RequestsPerMinute || c.RequestBurst != l.RequestBurst ||
		c.SignUpsPerMinute != l.SignUpsPerMinute || c.SignUpBurst != l.SignUpBurst {
		t.Errorf("config's Auth limits = %+v, want the API's %+v", c, l)
	}
	if got := authLimits(&config.Config{Auth: config.Defaults().Auth}); got != l {
		t.Errorf("authLimits of the defaults = %+v, want %+v", got, l)
	}
}

// The configured burst is the one the Auth API applies per address.
func TestAuthLimitsConfigured(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	calls := func(env map[string]string, n int) (limited int) {
		t.Helper()
		vars := map[string]string{
			"ARUMA_DATABASE_URL": "postgres://unused",
			"ARUMA_PUBLIC_URL":   "https://aruma.test",
			"ARUMA_MASTER_KEY":   "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		}
		for k, v := range env {
			vars[k] = v
		}
		cfg, err := config.Load("", func(k string) string { return vars[k] })
		if err != nil {
			t.Fatal(err)
		}
		a, err := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", pool)
		if err != nil {
			t.Fatal(err)
		}
		h := a.Server.Handler()
		for range n {
			// No API key: refused before the database, after the per-address bucket.
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/identitytoolkit.googleapis.com/v1/accounts:signInWithPassword", strings.NewReader(`{}`)))
			if strings.Contains(rec.Body.String(), "TOO_MANY_ATTEMPTS_TRY_LATER") {
				limited++
			}
		}
		return limited
	}
	if n := calls(map[string]string{"ARUMA_AUTH_REQUEST_BURST": "3", "ARUMA_AUTH_REQUESTS_PER_MINUTE": "1"}, 5); n != 2 {
		t.Errorf("a burst of 3: %d of 5 calls limited, want 2", n)
	}
	if n := calls(nil, 5); n != 0 {
		t.Errorf("the default burst of 100: %d of 5 calls limited", n)
	}
}
