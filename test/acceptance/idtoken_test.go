package acceptance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/osama300/aurma-project/internal/platform/idtoken"
)

// The ID token contract as Realtime Database will use it (R4): a token from a real sign-in
// verifies through App.IDTokens, and each way of ending the user's sessions, through the Admin
// API, the console or the user's own client, reaches an App.Revocations subscriber within a
// second and is refused by the next verification.
func TestIDTokenContract(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, nil)
	p := e.adminProject("chat-app")
	number := p.ref().Number
	sub := e.App.Revocations.Subscribe(number)
	defer sub.Close()

	p.signUp("r@example.com", "secret1")
	in := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "r@example.com", "password": "secret1", "returnSecureToken": true}))
	uid := in.str("localId")
	c, err := e.App.IDTokens.Verify(ctx, number, in.str("idToken"))
	if err != nil {
		t.Fatal(err)
	}
	if c.UID != uid || c.SignInProvider != "password" || c.Email != "r@example.com" || c.Token["user_id"] != uid {
		t.Fatalf("claims %+v", c)
	}
	if _, err := e.App.IDTokens.Verify(ctx, number+1, in.str("idToken")); !errors.Is(err, idtoken.ErrInvalid) {
		t.Fatalf("another project: %v", err)
	}

	// await waits up to a second from start for the revocation that call caused.
	await := func(what string, call func(), want idtoken.Cause) idtoken.Revocation {
		t.Helper()
		start := time.Now()
		call()
		select {
		case r := <-sub.C():
			if r.UID != uid || r.Cause != want || r.Project != number {
				t.Fatalf("%s: got %+v, want %s", what, r, want)
			}
			t.Logf("%s: %s after %v", what, r.Cause, time.Since(start))
			return r
		case <-time.After(time.Until(start.Add(time.Second))):
			t.Fatalf("%s: no revocation within 1s", what)
		}
		return idtoken.Revocation{}
	}

	// Disabled through the Admin API, as firebase-admin's updateUser({disabled: true}).
	r := await("admin disable", func() {
		ok(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": uid, "disableUser": true}))
	}, idtoken.Disabled)
	if !r.Revokes(number, c) {
		t.Fatal("the revocation does not end the session")
	}
	if _, err := e.App.IDTokens.Verify(ctx, number, in.str("idToken")); !errors.Is(err, idtoken.ErrDisabled) {
		t.Fatalf("after disabling: %v", err)
	}
	ok(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": uid, "disableUser": false}))
	if _, err := e.App.IDTokens.Verify(ctx, number, in.str("idToken")); err != nil {
		t.Fatalf("enabled again: %v", err)
	}

	// Sessions revoked from the console.
	await("console revoke", func() {
		p.owner.mustOK(p.owner.do("POST", consoleAuth("chat-app")+"/users/"+uid+":revokeSessions", nil))
	}, idtoken.TokensRevoked)
	if _, err := e.App.IDTokens.Verify(ctx, number, in.str("idToken")); !errors.Is(err, idtoken.ErrRevoked) {
		t.Fatalf("after revoking: %v", err)
	}

	// The user's own password change: the old session ends, the one it returns does not.
	fresh := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "r@example.com", "password": "secret1", "returnSecureToken": true}))
	var changed *Response
	r = await("password change", func() {
		changed = ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": fresh.str("idToken"), "password": "secret2", "returnSecureToken": true}))
	}, idtoken.PasswordChanged)
	nc, err := e.App.IDTokens.Verify(ctx, number, changed.str("idToken"))
	if err != nil {
		t.Fatalf("the session the change returned: %v", err)
	}
	if r.Revokes(number, nc) {
		t.Fatal("the password change ends the session it returned")
	}

	// Deleted through the Admin API.
	await("admin delete", func() {
		ok(t, p.admin(p.sa, "accounts:delete", map[string]any{"localId": uid}))
	}, idtoken.Deleted)
	if _, err := e.App.IDTokens.Verify(ctx, number, changed.str("idToken")); !errors.Is(err, idtoken.ErrUserNotFound) {
		t.Fatalf("after deleting: %v", err)
	}
}
