package core

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/store/storetest"
)

// housekeepingRows reads which of the named rows a table still holds.
func housekeepingRows(t *testing.T, pool *pgxpool.Pool, query string) map[string]bool {
	t.Helper()
	rows, err := pool.Query(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func wantRows(t *testing.T, what string, got map[string]bool, keep, gone []string) {
	t.Helper()
	for _, k := range keep {
		if !got[k] {
			t.Errorf("%s: %s was purged, but something still reads it", what, k)
		}
	}
	for _, g := range gone {
		if got[g] {
			t.Errorf("%s: %s was kept past its retention", what, g)
		}
	}
}

// Housekeep deletes the console sessions and CLI tokens that stopped working, and the audit log
// and change log rows past their retention, and nothing else.
func TestHousekeepControlRetention(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	_, p := rulesOwner(t, s, pool, "owner@example.com", "alpha-app")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	account := uuid.New()
	exec(`INSERT INTO control.accounts (id, email, password_hash) VALUES ($1, 'h@example.com', 'x')`, account)

	// Console sessions, named by user_agent: absolute expiry is the bound of every session.
	for _, x := range []struct{ name, idle, absolute, revoked string }{
		{"live", "now() + interval '10 minutes'", "now() + interval '7 hours'", "NULL"},
		{"idle-today", "now() - interval '1 hour'", "now() + interval '7 hours'", "NULL"},
		{"revoked-today", "now() + interval '10 minutes'", "now() + interval '7 hours'", "now() - interval '1 hour'"},
		{"ended-hours-ago", "now() - interval '9 hours'", "now() - interval '2 hours'", "NULL"},
		{"ended-days-ago", "now() - interval '3 days'", "now() - interval '2 days'", "NULL"},
		{"ended-year-ago", "now() - interval '400 days'", "now() - interval '400 days'", "now() - interval '401 days'"},
	} {
		exec(`INSERT INTO control.sessions (account_id, token_hash, user_agent, created_at, idle_expires_at, absolute_expires_at, revoked_at)
			VALUES ($1, uuid_send(gen_random_uuid()), $2, now() - interval '500 days', `+x.idle+`, `+x.absolute+`, `+x.revoked+`)`, account, x.name)
	}
	// CLI tokens, named by name: an expired one stays listed for 30 days, a revoked one a day.
	for _, x := range []struct{ name, expires, revoked string }{
		{"forever", "NULL", "NULL"},
		{"until-next-year", "now() + interval '365 days'", "NULL"},
		{"expired-last-week", "now() - interval '7 days'", "NULL"},
		{"expired-long-ago", "now() - interval '31 days'", "NULL"},
		{"revoked-an-hour-ago", "NULL", "now() - interval '1 hour'"},
		{"revoked-days-ago", "NULL", "now() - interval '2 days'"},
		{"revoked-and-expired", "now() - interval '40 days'", "now() - interval '45 days'"},
	} {
		exec(`INSERT INTO control.access_tokens (account_id, name, prefix, token_hash, created_at, expires_at, revoked_at)
			VALUES ($1, $2, 'p', uuid_send(gen_random_uuid()), now() - interval '500 days', `+x.expires+`, `+x.revoked+`)`, account, x.name)
	}
	// Audit and change log rows, named by action and type.
	for _, age := range []string{"1 hour", "3 days", "8 days", "89 days", "91 days", "400 days"} {
		exec(`INSERT INTO control.audit_log (at, actor_type, project_number, action, result)
			VALUES (now() - $1::text::interval, 'system', $2, $1::text, 'ok')`, age, p.Number)
		exec(`INSERT INTO events.changes (project_number, service, type, subject, committed_at)
			VALUES ($2, 'auth', $1::text, 'u', now() - $1::text::interval)`, age, p.Number)
	}

	// Retention 0 keeps the logs forever; the credentials go whatever it says.
	c, err := s.Housekeep(ctx, Retention{})
	if err != nil {
		t.Fatal(err)
	}
	if c["control.sessions"] != 2 || c["control.access_tokens"] != 3 || c["control.audit_log"] != 0 || c["events.changes"] != 0 {
		t.Errorf("Housekeep(0) = %v", c)
	}
	wantRows(t, "control.sessions", housekeepingRows(t, pool, `SELECT user_agent FROM control.sessions`),
		[]string{"live", "idle-today", "revoked-today", "ended-hours-ago"}, []string{"ended-days-ago", "ended-year-ago"})
	wantRows(t, "control.access_tokens", housekeepingRows(t, pool, `SELECT name FROM control.access_tokens`),
		[]string{"forever", "until-next-year", "expired-last-week", "revoked-an-hour-ago"},
		[]string{"expired-long-ago", "revoked-days-ago", "revoked-and-expired"})
	if n := len(housekeepingRows(t, pool, `SELECT action FROM control.audit_log WHERE action LIKE '% %'`)); n != 6 {
		t.Errorf("retention 0 left %d audit rows of 6", n)
	}

	c, err = s.Housekeep(ctx, Retention{AuditDays: 90, EventsDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if c["control.audit_log"] != 2 || c["events.changes"] != 3 || c["control.sessions"] != 0 {
		t.Errorf("Housekeep(90, 30) = %v", c)
	}
	wantRows(t, "control.audit_log", housekeepingRows(t, pool, `SELECT action FROM control.audit_log`),
		[]string{"1 hour", "3 days", "8 days", "89 days"}, []string{"91 days", "400 days"})
	wantRows(t, "events.changes", housekeepingRows(t, pool, `SELECT type FROM events.changes`),
		[]string{"1 hour", "3 days", "8 days"}, []string{"89 days", "91 days", "400 days"})

	// The database keeps the last seven days of the audit log whatever the server asks: the
	// batch holding a younger row fails whole.
	if _, err := s.Housekeep(ctx, Retention{AuditDays: 1}); err == nil {
		t.Error("the audit log let housekeeping delete a row younger than 7 days")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM control.audit_log WHERE action = '1 hour'`); err == nil {
		t.Error("the audit log let a recent row be deleted")
	}
	if _, err := pool.Exec(ctx, `UPDATE control.audit_log SET action = 'x' WHERE action = '89 days'`); err == nil {
		t.Error("the audit log let a row be updated")
	}
	wantRows(t, "control.audit_log", housekeepingRows(t, pool, `SELECT action FROM control.audit_log`),
		[]string{"1 hour", "3 days", "8 days", "89 days"}, nil)
}

// Housekeep deletes in batches: a backlog larger than one batch goes in one pass.
func TestHousekeepBatches(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	if _, err := pool.Exec(ctx, `INSERT INTO events.changes (project_number, service, type, subject, committed_at)
		SELECT 482915736204, 'auth', 'user.created', 'u' || i, now() - interval '40 days' FROM generate_series(1, 2500) i`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO events.changes (project_number, service, type, subject) VALUES (482915736204, 'auth', 'user.created', 'fresh')`); err != nil {
		t.Fatal(err)
	}
	c, err := s.Housekeep(ctx, Retention{EventsDays: 30})
	if err != nil || c["events.changes"] != 2500 {
		t.Fatalf("Housekeep = %v, %v", c, err)
	}
	wantRows(t, "events.changes", housekeepingRows(t, pool, `SELECT subject FROM events.changes`), []string{"fresh"}, []string{"u1", "u2500"})
}

// An expired project's change log goes with it; another project's stays.
func TestPurgeExpiredRemovesTheChangeLog(t *testing.T) {
	ctx := context.Background()
	pool := storetest.NewPool(t)
	s := rulesService(t, pool, rulesDomain, nil)
	pa, a := rulesOwner(t, s, pool, "a@example.com", "alpha-app")
	_, b := rulesOwner(t, s, pool, "b@example.com", "bravo-app")
	for _, p := range []Project{a, b} {
		if _, err := pool.Exec(ctx, `INSERT INTO events.changes (project_number, service, type, subject)
			SELECT $1, 'auth', 'user.created', 'u' || i FROM generate_series(1, 1500) i`, p.Number); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DeleteProject(ctx, pa, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	count := func(p Project) (n int) {
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM events.changes WHERE project_number = $1`, p.Number).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(a) != 1500 {
		t.Fatal("a project in its 30 days of deletion lost its change log")
	}
	if _, err := pool.Exec(ctx, `UPDATE control.projects SET expire_time = now() - interval '1 second' WHERE project_number = $1`, a.Number); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(a); n != 0 {
		t.Errorf("%d change log rows left for the purged project", n)
	}
	if n := count(b); n != 1500 {
		t.Errorf("the other project has %d change log rows, want 1500", n)
	}
}
