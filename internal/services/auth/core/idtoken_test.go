package core

import (
	"context"
	gocrypto "crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/services/auth/queries"
	"github.com/arumadev/aruma/internal/services/auth/tokens"
	"github.com/arumadev/aruma/internal/store/storetest"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// idEnv is an Auth service on a fresh database with two projects.
type idEnv struct {
	s    *Service
	pool *pgxpool.Pool
	a, b Project
}

func newIDEnv(t testing.TB) *idEnv {
	t.Helper()
	ctx := context.Background()
	pool := storetest.NewPool(t)
	keys, err := crypto.NewKeys([]byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	hasher := crypto.NewHasher(crypto.Argon2Params{Memory: 64, Time: 1, Threads: 1}, 2)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	control := tcore.New(pool, keys, hasher, tcore.Options{}, logger)
	var owner string
	if err := pool.QueryRow(ctx, `INSERT INTO control.accounts (email, password_hash) VALUES ('o@example.com', 'x') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	e := &idEnv{s: New(pool, control, keys, hasher, "http://aruma.test", logger), pool: pool,
		a: Project{Number: 482915736204, ID: "chat-app"}, b: Project{Number: 582915736204, ID: "other-app"}}
	for _, p := range []Project{e.a, e.b} {
		if _, err := pool.Exec(ctx, `INSERT INTO control.projects (project_number, project_id, display_name, owner_account_id, etag, auth_domain)
			VALUES ($1, $2, $2, $3, 'e', $2 || '.aruma.test')`, p.Number, p.ID, owner); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

// user creates a user with an email and password in p.
func (e *idEnv) user(t testing.TB, p Project, uid string) User {
	t.Helper()
	u, err := e.s.CreateUser(context.Background(), p, AdminUser{UID: uid, Email: ptr(uid + "@example.com"), Password: ptr("secret1")})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// token mints an ID token for u as a password sign-in at authTime.
func (e *idEnv) token(t testing.TB, p Project, u User, authTime time.Time) string {
	t.Helper()
	sg, err := e.s.idSigner(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := e.s.mint(sg, p, u, "password", json.RawMessage(`{"role":"admin"}`), authTime)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func (e *idEnv) verify(p Project, tok string) (*idtoken.Claims, error) {
	return e.s.IDTokenVerifier().Verify(context.Background(), p.Number, tok)
}

func TestVerifyIDToken(t *testing.T) {
	ctx := context.Background()
	e := newIDEnv(t)
	u := e.user(t, e.a, "u1")
	now := time.Now()
	good := e.token(t, e.a, u, now)

	c, err := e.verify(e.a, good)
	if err != nil {
		t.Fatal(err)
	}
	if c.UID != "u1" || c.SignInProvider != "password" || c.Email != "u1@example.com" || c.EmailVerified ||
		c.AuthTime.Unix() != now.Unix() || c.Expires.Sub(c.IssuedAt) != TokenLifetime {
		t.Fatalf("claims %+v", c)
	}
	if c.Token["role"] != "admin" || c.Token["sub"] != "u1" || c.Token["aud"] != "chat-app" {
		t.Fatalf("auth.token %v", c.Token)
	}

	other := e.user(t, e.b, "u1")
	foreign := e.token(t, e.b, other, now)
	parts := strings.Split(good, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sig[0] ^= 1
	forgedKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	edited := strings.Replace(string(payload), `"sub":"u1"`, `"sub":"u2"`, 1)

	cases := []struct {
		name    string
		project Project
		token   string
		want    error
	}{
		{"another project's token", e.a, foreign, idtoken.ErrInvalid},
		{"presented to another project", e.b, good, idtoken.ErrInvalid},
		{"unknown project", Project{Number: 999999999999}, good, idtoken.ErrInvalid},
		{"wrong signature", e.a, parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(sig), idtoken.ErrInvalid},
		{"signed by another key, same kid", e.a, signRaw(t, forgedKey, parts[0], parts[1]), idtoken.ErrInvalid},
		{"edited payload", e.a, parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(edited)) + "." + parts[2], idtoken.ErrInvalid},
		{"garbage", e.a, "a.b.c", idtoken.ErrInvalid},
		{"session cookie", e.a, e.cookie(t, good), idtoken.ErrInvalid},
	}
	for _, tc := range cases {
		if _, err := e.verify(tc.project, tc.token); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}

	// Expired: an hour and a second later. Expiry is told apart, for expired_token.
	e.s.now = func() time.Time { return now.Add(TokenLifetime + time.Second) }
	if _, err := e.verify(e.a, good); !errors.Is(err, idtoken.ErrExpired) || idtoken.Status(err) != "expired_token" {
		t.Fatalf("expired: %v", err)
	}
	e.s.now = time.Now
	if _, err := e.verify(e.a, e.token(t, e.a, u, now.Add(time.Hour))); !errors.Is(err, idtoken.ErrInvalid) {
		t.Fatalf("auth_time an hour ahead: %v", err)
	}

	// revokeRefreshTokens: the token's session began before the new tokensValidAfter.
	if _, err := e.verify(e.a, good); err != nil {
		t.Fatal(err) // and its state is now cached
	}
	revoked, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", RevokeTokens: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.verify(e.a, good); !errors.Is(err, idtoken.ErrRevoked) || idtoken.Status(err) != "invalid_token" {
		t.Fatalf("after revokeRefreshTokens: %v", err)
	}
	// A session that starts at the revocation point, as the one a password change returns, is
	// valid.
	after := e.token(t, e.a, revoked, revoked.TokensValidAfter)
	if _, err := e.verify(e.a, after); err != nil {
		t.Fatalf("session started at the revocation point: %v", err)
	}

	// Disabled, then enabled again: both seen at once despite the cache.
	if _, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", Disabled: ptrBool(true)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.verify(e.a, after); !errors.Is(err, idtoken.ErrDisabled) {
		t.Fatalf("disabled: %v", err)
	}
	if _, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", Disabled: ptrBool(false)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.verify(e.a, after); err != nil {
		t.Fatalf("enabled again: %v", err)
	}

	// Deleted; then the uid comes back, and the earlier user's token is not the new user's.
	if err := e.s.DeleteUser(ctx, e.a, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.verify(e.a, after); !errors.Is(err, idtoken.ErrUserNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	// Past the second the token was issued in: uids are compared with iat at second precision.
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(2*time.Second + 10*time.Millisecond)))
	reborn := e.user(t, e.a, "u1")
	if _, err := e.verify(e.a, after); !errors.Is(err, idtoken.ErrRevoked) {
		t.Fatalf("an earlier user's token for a reused uid: %v", err)
	}
	if _, err := e.verify(e.a, e.token(t, e.a, reborn, time.Now())); err != nil {
		t.Fatalf("the new user: %v", err)
	}
}

// Developer claims keep their numbers' text from the user record to the verified claims, as
// Firebase's tokens do: deployed rules read 9007199254740993 as that int (S7-R,
// test/oracle/rules/production/deployed-firestore.json), which a float64 cannot hold.
func TestIDTokenClaimNumbers(t *testing.T) {
	ctx := context.Background()
	e := newIDEnv(t)
	e.user(t, e.a, "u1")
	claims := json.RawMessage(`{"n_two":2.0,"n_frac":2.5,"n_big":9007199254740993,"m":{"n":3,"big":-9007199254740993},"l":[3,9007199254740995]}`)
	u, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", CustomClaims: &claims})
	if err != nil {
		t.Fatal(err)
	}
	tok := e.token(t, e.a, u, time.Now())
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"n_big":9007199254740993`, `"n_frac":2.5`, `"big":-9007199254740993`, `[3,9007199254740995]`} {
		if !strings.Contains(string(payload), want) {
			t.Errorf("payload %s: no %s", payload, want)
		}
	}
	c, err := e.verify(e.a, tok)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := c.Token["m"].(map[string]any)
	l, _ := c.Token["l"].([]any)
	if c.Token["n_big"] != json.Number("9007199254740993") || c.Token["n_frac"] != json.Number("2.5") ||
		m["big"] != json.Number("-9007199254740993") || len(l) != 2 || l[1] != json.Number("9007199254740995") {
		t.Fatalf("auth.token %v", c.Token)
	}
	if n, ok := c.Token["n_two"].(json.Number); !ok || (n != "2.0" && n != "2") {
		t.Fatalf("n_two %#v", c.Token["n_two"])
	}
	if c.Token["role"] != "admin" {
		t.Fatalf("token claims %v", c.Token)
	}
}

// A normal rotation keeps the retired key's tokens valid for their lifetime; an emergency one
// ends the active key's tokens at once.
func TestVerifyIDTokenAcrossRotation(t *testing.T) {
	ctx := context.Background()
	e := newIDEnv(t)
	u := e.user(t, e.a, "u1")
	before := e.token(t, e.a, u, time.Now())
	if _, err := e.verify(e.a, before); err != nil {
		t.Fatal(err)
	}
	if err := e.s.RotateSigningKey(ctx, e.a, string(tokens.IDToken), false); err != nil {
		t.Fatal(err)
	}
	if _, err := e.verify(e.a, before); err != nil {
		t.Fatalf("retired key in its grace period: %v", err)
	}
	after := e.token(t, e.a, u, time.Now())
	if kidOf(after) == kidOf(before) {
		t.Fatal("rotation did not change the key")
	}
	if _, err := e.verify(e.a, after); err != nil {
		t.Fatalf("new active key: %v", err)
	}
	if err := e.s.RotateSigningKey(ctx, e.a, string(tokens.IDToken), true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.verify(e.a, after); !errors.Is(err, idtoken.ErrInvalid) {
		t.Fatalf("emergency rotation: %v", err)
	}
	if _, err := e.verify(e.a, before); err != nil {
		t.Fatalf("an emergency rotation of another key ended the retired key: %v", err)
	}
}

// Once warm, verification touches no database connection.
func TestVerifyIDTokenFromCache(t *testing.T) {
	e := newIDEnv(t)
	tok := e.token(t, e.a, e.user(t, e.a, "u1"), time.Now())
	if _, err := e.verify(e.a, tok); err != nil {
		t.Fatal(err)
	}
	before := e.pool.Stat().AcquireCount()
	for i := 0; i < 100; i++ {
		if _, err := e.verify(e.a, tok); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.pool.Stat().AcquireCount() - before; n != 0 {
		t.Fatalf("100 warm verifications acquired %d connections", n)
	}
}

// A load that overlaps a change is not kept, so it cannot pin the state from before it.
func TestUserStateLoadRacingAChange(t *testing.T) {
	ctx := context.Background()
	e := newIDEnv(t)
	e.user(t, e.a, "u1")
	k := userKey{e.a.Number, "", "u1"}
	kept := func() bool {
		e.s.states.mu.Lock()
		defer e.s.states.mu.Unlock()
		_, ok := e.s.states.m[k]
		return ok
	}
	q := e.s.q
	e.s.q = queries.New(&hookDB{DBTX: e.pool, during: func() { e.s.userChanged(e.a, "u1", idtoken.Disabled, time.Time{}) }})
	if _, err := e.s.states.get(ctx, e.s, e.a.Number, "", "u1"); err != nil {
		t.Fatal(err)
	}
	if kept() {
		t.Fatal("a load that raced a change was cached")
	}
	e.s.q = q
	if _, err := e.s.states.get(ctx, e.s, e.a.Number, "", "u1"); err != nil {
		t.Fatal(err)
	}
	if !kept() {
		t.Fatal("an undisturbed load was not cached")
	}
}

// hookDB runs during inside every QueryRow, as if another change committed meanwhile.
type hookDB struct {
	queries.DBTX
	during func()
}

func (h *hookDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	h.during()
	return h.DBTX.QueryRow(ctx, sql, args...)
}

// Every change that ends a user's sessions publishes one revocation after it commits; a
// change that ends none publishes nothing.
func TestRevocationEvents(t *testing.T) {
	ctx := context.Background()
	e := newIDEnv(t)
	sub := e.s.Revocations().Subscribe(e.a.Number)
	defer sub.Close()
	others := e.s.Revocations().Subscribe(e.b.Number)
	defer others.Close()
	next := func(want idtoken.Cause, uid string) idtoken.Revocation {
		t.Helper()
		select {
		case r := <-sub.C():
			if r.Cause != want || r.UID != uid || r.Project != e.a.Number {
				t.Fatalf("got %+v, want %s of %s", r, want, uid)
			}
			if (want == idtoken.Disabled || want == idtoken.Deleted) != r.ValidAfter.IsZero() {
				t.Fatalf("%s: ValidAfter %v", want, r.ValidAfter)
			}
			return r
		case <-time.After(time.Second):
			t.Fatalf("no %s revocation", want)
		}
		return idtoken.Revocation{}
	}
	none := func(what string) {
		t.Helper()
		select {
		case r := <-sub.C():
			t.Fatalf("%s published %+v", what, r)
		default:
		}
	}

	u := e.user(t, e.a, "u1")
	none("creating a user")
	if _, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", DisplayName: ptr("Name")}); err != nil {
		t.Fatal(err)
	}
	none("a profile change")

	if _, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", Disabled: ptrBool(true)}); err != nil {
		t.Fatal(err)
	}
	next(idtoken.Disabled, "u1")
	if _, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", Disabled: ptrBool(false)}); err != nil {
		t.Fatal(err)
	}
	none("enabling a user")

	upd, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", RevokeTokens: true})
	if err != nil {
		t.Fatal(err)
	}
	if r := next(idtoken.TokensRevoked, "u1"); !r.ValidAfter.Equal(upd.TokensValidAfter) {
		t.Fatalf("ValidAfter %v, user's %v", r.ValidAfter, upd.TokensValidAfter)
	}
	if _, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", Password: ptr("secret2")}); err != nil {
		t.Fatal(err)
	}
	next(idtoken.PasswordChanged, "u1")
	if _, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", Email: ptr("u1b@example.com")}); err != nil {
		t.Fatal(err)
	}
	next(idtoken.EmailChanged, "u1")

	// A change that rolls back publishes nothing.
	e.user(t, e.a, "u2")
	if _, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", Email: ptr("u2@example.com"), Disabled: ptrBool(true)}); !errors.Is(err, ErrEmailExists) {
		t.Fatalf("taken email: %v", err)
	}
	none("a failed update")

	// The user's own password change: the session it returns survives its revocation.
	u, err = e.s.GetUser(ctx, e.a, "u1")
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.s.Update(ctx, e.a, UpdateInput{IDToken: e.token(t, e.a, u, u.TokensValidAfter), Password: ptr("secret3")})
	if err != nil {
		t.Fatal(err)
	}
	r := next(idtoken.PasswordChanged, "u1")
	c, err := e.verify(e.a, res.Session.IDToken)
	if err != nil {
		t.Fatalf("the session the change returned: %v", err)
	}
	if r.Revokes(e.a.Number, c) {
		t.Fatal("the revocation ends the session the change returned")
	}

	// Password reset by email link.
	link, err := e.s.GeneratePasswordResetLink(ctx, e.a, "u1b@example.com", ActionCodeSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.ResetPassword(ctx, e.a, oobCode(t, link), "secret4"); err != nil {
		t.Fatal(err)
	}
	next(idtoken.PasswordChanged, "u1")

	// Email change by link.
	link, err = e.s.GenerateVerifyAndChangeEmailLink(ctx, e.a, "u1b@example.com", "u1c@example.com", ActionCodeSettings{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.s.ApplyActionCode(ctx, e.a, oobCode(t, link), "en", ""); err != nil {
		t.Fatal(err)
	}
	next(idtoken.EmailChanged, "u1")

	// Deleted by an admin, and by the user (accounts:delete).
	if err := e.s.DeleteUser(ctx, e.a, "u2"); err != nil {
		t.Fatal(err)
	}
	next(idtoken.Deleted, "u2")
	u, _ = e.s.GetUser(ctx, e.a, "u1")
	if err := e.s.Delete(ctx, e.a, e.token(t, e.a, u, u.TokensValidAfter)); err != nil {
		t.Fatal(err)
	}
	next(idtoken.Deleted, "u1")

	select {
	case r := <-others.C():
		t.Fatalf("another project's subscriber got %+v", r)
	default:
	}
}

func (e *idEnv) cookie(t testing.TB, idToken string) string {
	t.Helper()
	c, err := e.s.CreateSessionCookie(context.Background(), e.a, idToken, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// signRaw signs header.payload with key, keeping the header and so its kid.
func signRaw(t testing.TB, key *rsa.PrivateKey, header, payload string) string {
	t.Helper()
	digest := sha256.Sum256([]byte(header + "." + payload))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, gocrypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return header + "." + payload + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func kidOfSegment(header string) string {
	hb, _ := base64.RawURLEncoding.DecodeString(header)
	var h struct {
		Kid string `json:"kid"`
	}
	json.Unmarshal(hb, &h)
	return h.Kid
}

func kidOf(tok string) string { return kidOfSegment(strings.Split(tok, ".")[0]) }

func oobCode(t testing.TB, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	code := u.Query().Get("oobCode")
	if code == "" {
		t.Fatalf("no oobCode in %s", link)
	}
	return code
}

func ptrBool(b bool) *bool { return &b }

// BenchmarkVerifyIDToken measures verification warm (keys, project id and user state cached:
// no database) and after a change to the user (its state and the project id read again).
func BenchmarkVerifyIDToken(b *testing.B) {
	e := newIDEnv(b)
	tok := e.token(b, e.a, e.user(b, e.a, "u1"), time.Now())
	ctx := context.Background()
	v := e.s.IDTokenVerifier()
	if _, err := v.Verify(ctx, e.a.Number, tok); err != nil {
		b.Fatal(err)
	}
	run := func(b *testing.B, before func()) {
		acquired := e.pool.Stat().AcquireCount()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			before()
			if _, err := v.Verify(ctx, e.a.Number, tok); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(e.pool.Stat().AcquireCount()-acquired)/float64(b.N), "queries/op")
	}
	b.Run("warm", func(b *testing.B) { run(b, func() {}) })
	b.Run("uncached", func(b *testing.B) {
		run(b, func() {
			e.s.userChanged(e.a, "u1", "", time.Time{})
			e.s.ids = projectIDs{}
		})
	})
}
