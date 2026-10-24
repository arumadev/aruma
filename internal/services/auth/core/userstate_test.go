package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/idtoken"
)

// Users.State answers as the Verifier would for a token of the same session, at every step of a
// user's life, and sees each change at once despite the cache (ADR-022 §7).
func TestUsersState(t *testing.T) {
	ctx := context.Background()
	e := newIDEnv(t)
	u := e.user(t, e.a, "u1")
	now := time.Now()
	c, err := e.verify(e.a, e.token(t, e.a, u, now))
	if err != nil {
		t.Fatal(err)
	}
	users := e.s.Users()
	admits := func() error {
		t.Helper()
		st, err := users.State(ctx, e.a.Number, "u1")
		if err != nil {
			t.Fatal(err)
		}
		return st.Admits(c.AuthTime, c.IssuedAt)
	}
	st, err := users.State(ctx, e.a.Number, "u1")
	if err != nil || !st.Exists || st.Disabled || st.Created.Unix() != u.CreatedAt.Unix() {
		t.Fatalf("State = %+v, %v", st, err)
	}
	if err := admits(); err != nil {
		t.Fatalf("a live user: %v", err)
	}
	// Another project's user of the same uid is another user.
	if st, err := users.State(ctx, e.b.Number, "u1"); err != nil || st.Exists {
		t.Errorf("another project: %+v, %v", st, err)
	}
	if _, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", Disabled: ptrBool(true)}); err != nil {
		t.Fatal(err)
	}
	if err := admits(); !errors.Is(err, idtoken.ErrDisabled) {
		t.Errorf("disabled: %v", err)
	}
	if _, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", Disabled: ptrBool(false)}); err != nil {
		t.Fatal(err)
	}
	if err := admits(); err != nil {
		t.Errorf("enabled again: %v", err)
	}
	// revokeRefreshTokens moves tokensValidAfter past the session's sign-in.
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second + 10*time.Millisecond)))
	if _, err := e.s.UpdateUser(ctx, e.a, AdminUser{UID: "u1", RevokeTokens: true}); err != nil {
		t.Fatal(err)
	}
	if err := admits(); !errors.Is(err, idtoken.ErrRevoked) {
		t.Errorf("revoked: %v", err)
	}
	if err := e.s.DeleteUser(ctx, e.a, "u1"); err != nil {
		t.Fatal(err)
	}
	if err := admits(); !errors.Is(err, idtoken.ErrUserNotFound) {
		t.Errorf("deleted: %v", err)
	}
}
