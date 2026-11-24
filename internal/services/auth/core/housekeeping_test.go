package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/auth/queries"
)

// signIn starts a password session for uid and returns its refresh token.
func (e *idEnv) signIn(t *testing.T, p Project, uid string) string {
	t.Helper()
	sess, err := e.s.SignInWithPassword(context.Background(), p, uid+"@example.com", "secret1")
	if err != nil {
		t.Fatal(err)
	}
	return sess.RefreshToken
}

// exec runs SQL on the test database.
func (e *idEnv) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

// refreshRow reports whether the refresh token's row exists.
func (e *idEnv) refreshRow(t *testing.T, p Project, refresh string) bool {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.refresh_tokens WHERE project_number = $1 AND token_hash = $2`,
		p.Number, e.s.keys.HashToken("auth-refresh:"+refresh)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// age moves a refresh token's creation, and its end when it has one, back by days.
func (e *idEnv) age(t *testing.T, p Project, refresh string, days int) {
	t.Helper()
	e.exec(t, `UPDATE auth.refresh_tokens SET created_at = created_at - make_interval(days => $3), auth_time = auth_time - make_interval(days => $3),
		ended_at = ended_at - make_interval(days => $3) WHERE project_number = $1 AND token_hash = $2`,
		p.Number, e.s.keys.HashToken("auth-refresh:"+refresh), days)
}

// Housekeep purges a refresh token 30 days after it stopped working, counted from the deletion
// of its user or the revocation of its session, never from its creation, and never purges a
// live token, however old: refresh tokens do not expire (ADR-004). Until the purge, a refresh
// answers USER_NOT_FOUND or TOKEN_EXPIRED, the codes that sign the client out.
func TestHousekeepRefreshTokens(t *testing.T) {
	ctx := context.Background()
	e := newIDEnv(t)
	keep := Retention{Batch: 2}
	for _, uid := range []string{"live", "revoked", "deleted"} {
		e.user(t, e.a, uid)
	}
	// Accounts from long ago: a session older than its user would be another user's.
	e.exec(t, `UPDATE auth.users SET created_at = created_at - interval '500 days', tokens_valid_after = tokens_valid_after - interval '500 days'
		WHERE project_number = $1`, e.a.Number)
	live := e.signIn(t, e.a, "live")
	e.age(t, e.a, live, 400)
	revoked, revokedAgain := e.signIn(t, e.a, "revoked"), e.signIn(t, e.a, "revoked")
	deleted, deletedAgain := e.signIn(t, e.a, "deleted"), e.signIn(t, e.a, "deleted")
	for _, r := range []string{revoked, revokedAgain, deleted, deletedAgain} {
		e.age(t, e.a, r, 60)
	}
	// Both end now: before, the purge counted 30 days from creation and took them at once.
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second + 10*time.Millisecond)))
	if _, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "revoked", RevokeTokens: true}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.DeleteUser(ctx, e.a, "deleted"); err != nil {
		t.Fatal(err)
	}
	// A session the revoking user starts afterwards is live.
	fresh := e.signIn(t, e.a, "revoked")

	c, err := e.s.Housekeep(ctx, keep)
	if err != nil {
		t.Fatal(err)
	}
	if c["auth.refresh_tokens"] != 0 {
		t.Errorf("purged %d refresh tokens that ended today", c["auth.refresh_tokens"])
	}
	for name, r := range map[string]string{"live": live, "revoked": revoked, "revoked again": revokedAgain,
		"deleted": deleted, "deleted again": deletedAgain, "fresh": fresh} {
		if !e.refreshRow(t, e.a, r) {
			t.Errorf("the %s token was purged", name)
		}
	}
	if _, err := e.s.Refresh(ctx, e.a, revoked); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("refresh of a revoked session = %v, want TOKEN_EXPIRED", err)
	}
	if _, err := e.s.Refresh(ctx, e.a, deleted); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("refresh of a deleted user's session = %v, want USER_NOT_FOUND", err)
	}
	for _, r := range []string{live, fresh} {
		if _, err := e.s.Refresh(ctx, e.a, r); err != nil {
			t.Errorf("refresh of a live session: %v", err)
		}
	}

	// 30 days after they ended, they go; the live ones stay.
	e.age(t, e.a, revokedAgain, 31)
	e.age(t, e.a, deletedAgain, 31)
	c, err = e.s.Housekeep(ctx, keep)
	if err != nil {
		t.Fatal(err)
	}
	if c["auth.refresh_tokens"] != 2 {
		t.Errorf("purged %d refresh tokens, want 2", c["auth.refresh_tokens"])
	}
	for name, r := range map[string]string{"revoked": revokedAgain, "deleted": deletedAgain} {
		if e.refreshRow(t, e.a, r) {
			t.Errorf("the %s token outlived its 30 days", name)
		}
	}
	for name, r := range map[string]string{"live": live, "fresh": fresh} {
		if !e.refreshRow(t, e.a, r) {
			t.Errorf("the %s token was purged", name)
		}
	}
}

// The revocation trigger compares to the second, as Refresh does: the session a revoking
// transaction starts is dated at the revocation point and stays live.
func TestRevocationEndsOnlyEarlierSessions(t *testing.T) {
	ctx := context.Background()
	e := newIDEnv(t)
	e.user(t, e.a, "u1")
	before := e.signIn(t, e.a, "u1")
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := queries.New(tx)
	if _, err := q.RevokeUserTokens(ctx, queries.RevokeUserTokensParams{ProjectNumber: e.a.Number, TenantID: "", Uid: "u1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.CreateRefreshToken(ctx, queries.CreateRefreshTokenParams{TokenHash: []byte("after"), ProjectNumber: e.a.Number,
		TenantID: "", Uid: "u1", SignInProvider: "password"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	ended := map[string]bool{}
	rows, err := e.pool.Query(ctx, `SELECT token_hash = 'after'::bytea, ended_at IS NOT NULL FROM auth.refresh_tokens WHERE project_number = $1`, e.a.Number)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var after, end bool
		if err := rows.Scan(&after, &end); err != nil {
			t.Fatal(err)
		}
		if after {
			ended["after"] = end
		} else {
			ended["before"] = end
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !ended["before"] || ended["after"] {
		t.Errorf("ended = %v, want the earlier session ended and the later one live", ended)
	}
	if _, err := e.s.Refresh(ctx, e.a, before); !errors.Is(err, ErrTokenExpired) {
		t.Errorf("refresh of the earlier session = %v", err)
	}
}

// Housekeep purges sign-in audit rows past the retention, oldest first and in batches, and keeps
// them all with a retention of 0.
func TestHousekeepAuthAudit(t *testing.T) {
	ctx := context.Background()
	e := newIDEnv(t)
	for _, p := range []Project{e.a, e.b} {
		e.exec(t, `INSERT INTO auth.audit (project_number, at, event, result)
			SELECT $1, now() - interval '100 days' - make_interval(secs => i), 'signin.password', 'denied' FROM generate_series(1, 5) i`, p.Number)
		e.exec(t, `INSERT INTO auth.audit (project_number, at, event, result) VALUES ($1, now() - interval '89 days', 'recent', 'ok'), ($1, now(), 'now', 'ok')`, p.Number)
	}
	count := func() (old, recent int) {
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE event = 'signin.password'), count(*) FILTER (WHERE event IN ('recent', 'now')) FROM auth.audit`).Scan(&old, &recent); err != nil {
			t.Fatal(err)
		}
		return old, recent
	}
	if c, err := e.s.Housekeep(ctx, Retention{AuditDays: 0, Batch: 3}); err != nil || c["auth.audit"] != 0 {
		t.Fatalf("Housekeep with retention 0 = %v, %v", c, err)
	}
	if old, recent := count(); old != 10 || recent != 4 {
		t.Fatalf("retention 0 left %d old and %d recent rows", old, recent)
	}
	// Ten old rows in batches of three: four statements, one pass.
	c, err := e.s.Housekeep(ctx, Retention{AuditDays: 90, Batch: 3})
	if err != nil || c["auth.audit"] != 10 {
		t.Fatalf("Housekeep = %v, %v", c, err)
	}
	if old, recent := count(); old != 0 || recent != 4 {
		t.Errorf("retention 90 left %d old and %d recent rows", old, recent)
	}
}

// PurgeProject deletes every Auth row of the purged project and none of another project's.
func TestPurgeProjectAuthData(t *testing.T) {
	ctx := context.Background()
	e := newIDEnv(t)
	for _, p := range []Project{e.a, e.b} {
		for _, uid := range []string{"u1", "u2", "u3"} {
			e.user(t, p, uid)
			e.signIn(t, p, uid)
		}
		if _, err := e.s.idSigner(ctx, p); err != nil {
			t.Fatal(err)
		}
		e.exec(t, `INSERT INTO auth.audit (project_number, event, result) VALUES ($1, 'x', 'ok')`, p.Number)
	}
	tables := []string{"auth.users", "auth.user_passwords", "auth.user_providers", "auth.refresh_tokens", "auth.audit",
		"auth.signing_keys", "auth.project_config", "events.changes"}
	rows := func(p Project) map[string]int {
		out := map[string]int{}
		for _, table := range tables {
			var n int
			if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE project_number = $1`, p.Number).Scan(&n); err != nil {
				t.Fatal(err)
			}
			out[table] = n
		}
		return out
	}
	other := rows(e.b)
	for table, n := range rows(e.a) {
		if n == 0 && table != "events.changes" {
			t.Fatalf("the test wrote no %s row", table)
		}
	}
	if err := e.s.PurgeProject(ctx, e.a.Number); err != nil {
		t.Fatal(err)
	}
	for table, n := range rows(e.a) {
		// The change log is tenancy's to purge (PurgeExpired).
		if n != 0 && table != "events.changes" {
			t.Errorf("%d %s rows left for the purged project", n, table)
		}
	}
	for table, n := range rows(e.b) {
		if n != other[table] {
			t.Errorf("the other project has %d %s rows, had %d", n, table, other[table])
		}
	}
	// Idempotent: tenancy runs it at every pass.
	if err := e.s.PurgeProject(ctx, e.a.Number); err != nil {
		t.Fatal(err)
	}
}
