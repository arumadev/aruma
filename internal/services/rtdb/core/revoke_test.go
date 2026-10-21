package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/osama300/aurma-project/internal/platform/idtoken"
)

// tokens stands in for Auth's verifier: token -> claims, or the error it fails with.
type tokens struct {
	mu     sync.Mutex
	claims map[string]*idtoken.Claims
	errs   map[string]error
	calls  int
}

func (v *tokens) Verify(_ context.Context, project int64, token string) (*idtoken.Claims, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls++
	if err := v.errs[token]; err != nil {
		return nil, err
	}
	if c := v.claims[token]; c != nil && project == 482915736204 {
		return c, nil
	}
	return nil, idtoken.ErrInvalid
}

func (v *tokens) set(token string, c *idtoken.Claims, err error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if c != nil {
		v.claims[token] = c
	}
	v.errs[token] = err
}

func claimsFor(uid string, expires time.Time) *idtoken.Claims {
	return &idtoken.Claims{UID: uid, SignInProvider: "password", AuthTime: time.Unix(1_700_000_000, 0), Expires: expires,
		Token: map[string]any{"sub": uid, "user_id": uid, "firebase": map[string]any{"sign_in_provider": "password"}}}
}

func revocationService(t *testing.T) (*Service, *tokens, *idtoken.Hub) {
	t.Helper()
	v := &tokens{claims: map[string]*idtoken.Claims{}, errs: map[string]error{}}
	hub := idtoken.NewHub(2)
	store := newMemStore()
	store.rules = map[string]string{"chat-app-default-rtdb": `{"rules": {"users": {"$uid": {".read": "auth.uid == $uid", ".write": "auth.uid == $uid"}}, "open": {".read": true}}}`}
	s := newTestService(t, store, Options{IDTokens: v, Revocations: hub, Now: time.Now})
	return s, v, hub
}

// await waits for the recorder to hold n entries.
func await(t *testing.T, rec *recorder, n int) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec.mu.Lock()
		got := len(rec.log)
		rec.mu.Unlock()
		if got >= n || time.Now().After(deadline) {
			return rec.take()
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSignInWithAnIDToken(t *testing.T) {
	s, v, _ := revocationService(t)
	v.set("good", claimsFor("u1", time.Now().Add(time.Hour)), nil)
	v.set("expired", nil, idtoken.ErrExpired)
	v.set("outage", nil, errors.New("connection refused"))
	c, _ := listenWith(t, s, "chat-app-default-rtdb", Auth{})
	ctx := context.Background()

	if err := c.SignIn(ctx, "good"); err != nil {
		t.Fatal(err)
	}
	if a := c.AuthVariable(); a["uid"] != "u1" || a["provider"] != "password" || a["user_id"] != "u1" || a["token"].(map[string]any)["sub"] != "u1" {
		t.Errorf("auth variable: %v", a)
	}
	if !c.Authenticated() {
		t.Error("a signed-in user is not authenticated")
	}
	if _, err := set(c, "/users/u1/x", `1`, nil); err != nil {
		t.Errorf("own write: %v", err)
	}
	if err := c.SignIn(ctx, "expired"); idtoken.Status(err) != "expired_token" {
		t.Errorf("expired token: %v", err)
	}
	if err := c.SignIn(ctx, "forged"); idtoken.Status(err) != "invalid_token" {
		t.Errorf("unknown token: %v", err)
	}
	if err := c.SignIn(ctx, "outage"); err == nil || idtoken.Status(err) != "" {
		t.Errorf("outage: %v", err)
	}
	// No verifier, no users.
	plain := newTestService(t, newMemStore(), Options{})
	p := open(t, plain, "chat-app-default-rtdb", Auth{})
	if err := p.SignIn(ctx, "good"); !errors.Is(err, idtoken.ErrInvalid) {
		t.Errorf("without a verifier: %v", err)
	}
}

// A revocation that matches a session's token ends its sign-in: ac invalid_token, then the
// listens it may no longer read are cancelled. Other users' sessions are left alone.
func TestRevocationEndsTheSession(t *testing.T) {
	s, v, hub := revocationService(t)
	v.set("t1", claimsFor("u1", time.Now().Add(time.Hour)), nil)
	v.set("t2", claimsFor("u2", time.Now().Add(time.Hour)), nil)
	ctx := context.Background()
	a, recA := listenWith(t, s, "chat-app-default-rtdb", Auth{})
	b, recB := listenWith(t, s, "chat-app-default-rtdb", Auth{})
	if err := a.SignIn(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := b.SignIn(ctx, "t2"); err != nil {
		t.Fatal(err)
	}
	listenSync(t, a, recA, "/users/u1", "", 0, "")
	listenSync(t, a, recA, "/open", "", 0, "")
	listenSync(t, b, recB, "/users/u2", "", 0, "")
	recA.take()
	recB.take()
	if n := hub.Subscribers(482915736204); n != 1 {
		t.Fatalf("%d subscriptions for the project, want 1", n)
	}

	start := time.Now()
	hub.Publish(idtoken.Revocation{Project: 482915736204, UID: "u1", Cause: idtoken.Disabled})
	got := await(t, recA, 2)
	want(t, "revoked", got, "ac invalid_token Auth token is no longer valid.", "c users/u1")
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v", d)
	}
	if a.Auth().User != nil {
		t.Error("still signed in")
	}
	want(t, "the other user", recB.take())

	// A password change keeps the session it started (ValidAfter at its auth_time).
	hub.Publish(idtoken.Revocation{Project: 482915736204, UID: "u2", Cause: idtoken.PasswordChanged, ValidAfter: time.Unix(1_700_000_000, 0)})
	time.Sleep(50 * time.Millisecond)
	if b.Auth().User == nil {
		t.Error("a revocation that spares the session ended it")
	}
	b.Close()
	a.Close()
	deadline := time.Now().Add(time.Second)
	for hub.Subscribers(482915736204) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := hub.Subscribers(482915736204); n != 0 {
		t.Errorf("%d subscriptions left with no user session", n)
	}
}

// When the subscription falls behind, every session's token is verified again.
func TestRevocationOverflowVerifiesAgain(t *testing.T) {
	s, v, hub := revocationService(t)
	v.set("t1", claimsFor("u1", time.Now().Add(time.Hour)), nil)
	v.set("t2", claimsFor("u2", time.Now().Add(time.Hour)), nil)
	ctx := context.Background()
	a, recA := listenWith(t, s, "chat-app-default-rtdb", Auth{})
	b, _ := listenWith(t, s, "chat-app-default-rtdb", Auth{})
	a.SignIn(ctx, "t1")
	b.SignIn(ctx, "t2")
	v.set("t1", nil, idtoken.ErrDisabled)
	// More than the buffer of 2 at once, for users who have no session here.
	for range 5 {
		hub.Publish(idtoken.Revocation{Project: 482915736204, UID: "nobody", Cause: idtoken.Deleted})
	}
	want(t, "u1 after the overflow", await(t, recA, 1), "ac invalid_token Auth token is no longer valid.")
	if b.Auth().User == nil {
		t.Error("u2, still valid, was signed out")
	}
	if n := hub.Subscribers(482915736204); n != 1 {
		t.Errorf("%d subscriptions after the overflow, want 1", n)
	}
}

// A user's sign-in ends when its token expires: ac expired_token.
func TestSignInExpires(t *testing.T) {
	s, v, _ := revocationService(t)
	v.set("short", claimsFor("u1", time.Now().Add(150*time.Millisecond)), nil)
	a, rec := listenWith(t, s, "chat-app-default-rtdb", Auth{})
	if err := a.SignIn(context.Background(), "short"); err != nil {
		t.Fatal(err)
	}
	listenSync(t, a, rec, "/users/u1", "", 0, "")
	rec.take()
	want(t, "expired", await(t, rec, 2), "ac expired_token Auth token is expired.", "c users/u1")
	// A sign-in replaced before its expiry is not ended by it.
	v.set("short2", claimsFor("u1", time.Now().Add(100*time.Millisecond)), nil)
	v.set("long", claimsFor("u1", time.Now().Add(time.Hour)), nil)
	a.SignIn(context.Background(), "short2")
	a.SignIn(context.Background(), "long")
	time.Sleep(250 * time.Millisecond)
	want(t, "replaced", rec.take())
}
