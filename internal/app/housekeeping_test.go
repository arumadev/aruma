package app

import (
	"bytes"
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/store/storetest"
)

// One housekeeping pass applies the configured retention to every table, purges an expired
// project's Auth data, and reports what it purged in one log line and in a metric, the table
// names unredacted.
func TestHousekeep(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)
	env := map[string]string{
		"ARUMA_DATABASE_URL":                 "postgres://unused",
		"ARUMA_PUBLIC_URL":                   "https://aruma.test",
		"ARUMA_MASTER_KEY":                   "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"ARUMA_RETENTION_AUTH_AUDIT_DAYS":    "30",
		"ARUMA_RETENTION_CONTROL_AUDIT_DAYS": "0",
	}
	cfg, err := config.Load("", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	a, err := New(cfg, slog.New(slog.NewJSONHandler(&logs, nil)), "test", pool)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	const live, gone = 482915736204, 582915736204
	exec(`INSERT INTO control.accounts (id, email, password_hash) VALUES ('00000000-0000-7000-8000-000000000001', 'o@example.com', 'x')`)
	exec(`INSERT INTO control.projects (project_number, project_id, display_name, owner_account_id, etag, auth_domain, state, delete_time, expire_time)
		VALUES ($1, 'live-app', 'x', '00000000-0000-7000-8000-000000000001', 'e', 'd', 'ACTIVE', NULL, NULL),
		       ($2, 'gone-app', 'x', '00000000-0000-7000-8000-000000000001', 'e', 'd', 'DELETED', now() - interval '31 days', now() - interval '1 day')`, live, gone)
	for _, p := range []int64{live, gone} {
		exec(`INSERT INTO auth.users (project_number, uid, created_at, tokens_valid_after) VALUES ($1, 'u1', now() - interval '1 year', now() - interval '1 year')`, p)
		exec(`INSERT INTO auth.refresh_tokens (token_hash, project_number, uid, sign_in_provider, auth_time, created_at)
			VALUES ($1::bytea, $2, 'u1', 'password', now() - interval '1 year', now() - interval '1 year')`, []byte{byte(p % 251)}, p)
	}
	// A session revoked 31 days ago, and audit rows on either side of 30 days.
	exec(`INSERT INTO auth.refresh_tokens (token_hash, project_number, uid, sign_in_provider, auth_time, created_at, ended_at)
		VALUES ('ended'::bytea, $1, 'u1', 'password', now() - interval '1 year', now() - interval '1 year', now() - interval '31 days')`, live)
	exec(`INSERT INTO auth.audit (project_number, at, event, result) VALUES ($1, now() - interval '31 days', 'old', 'ok'), ($1, now() - interval '29 days', 'new', 'ok')`, live)
	exec(`INSERT INTO control.audit_log (at, actor_type, action, result) VALUES (now() - interval '400 days', 'system', 'old', 'ok')`)

	c := a.Housekeep(ctx)
	if c["auth.refresh_tokens"] != 1 || c["auth.audit"] != 1 || c["control.audit_log"] != 0 {
		t.Errorf("Housekeep = %v", c)
	}
	count := func(sql string, args ...any) (n int) {
		t.Helper()
		if err := pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM auth.refresh_tokens WHERE project_number = $1`, live); n != 1 {
		t.Errorf("the live project has %d refresh tokens, want its live one", n)
	}
	if n := count(`SELECT count(*) FROM auth.audit WHERE event = 'new'`); n != 1 {
		t.Error("an audit row inside the retention was purged")
	}
	if n := count(`SELECT count(*) FROM control.audit_log WHERE action = 'old'`); n != 1 {
		t.Error("control_audit_days 0 did not keep the audit log")
	}
	if n := count(`SELECT (SELECT count(*) FROM auth.users WHERE project_number = $1) + (SELECT count(*) FROM auth.refresh_tokens WHERE project_number = $1)`, gone); n != 0 {
		t.Errorf("%d Auth rows left of the expired project", n)
	}
	line := logs.String()
	if !strings.Contains(line, `"msg":"housekeeping"`) || !strings.Contains(line, "auth.refresh_tokens=1") || !strings.Contains(line, "auth.audit=1") {
		t.Errorf("housekeeping log: %s", line)
	}
	rec := httptest.NewRecorder()
	a.Server.PrivateHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `aruma_housekeeping_purged_rows_total{table="auth.audit"} 1`) {
		t.Errorf("/metrics lacks the purged rows:\n%s", rec.Body)
	}
}
