package acceptance

import (
	"context"
	"testing"
	"time"
)

// Regression tests for the Phase 2 security review (docs/security/review-phase2.md).

// H-1: signUp with an idToken links a password to the current user. It must not become a way
// around accounts:update's rules: an account that already has a password keeps it, and an
// account that already has an address keeps it unless the change is allowed there.
func TestAuthLinkPasswordCannotReplaceCredentials(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "victim@example.com", "password": "secret1"}))
	// A stolen or stale ID token: the sign-in is an hour old, too old for accounts:update.
	if _, err := e.Pool.Exec(context.Background(), `UPDATE auth.refresh_tokens SET auth_time = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	old := ok(t, p.refresh(up.str("refreshToken"))).str("id_token")
	authErr(t, p.call(itk+"accounts:update", map[string]any{"idToken": old, "password": "attacker1"}), "CREDENTIAL_TOO_OLD_LOGIN_AGAIN")

	// The same change through signUp with the token: refused, and nothing changes.
	authErr(t, p.call(itk+"accounts:signUp", map[string]any{"idToken": old, "email": "attacker@example.com", "password": "attacker1"}), "PROVIDER_ALREADY_LINKED")
	authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "attacker@example.com", "password": "attacker1"}), "INVALID_LOGIN_CREDENTIALS")
	in := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "victim@example.com", "password": "secret1"}))
	assertEqual(t, "uid", in.str("localId"), up.str("localId"))
	ok(t, p.refresh(up.str("refreshToken")))

	// A user with an address but no password, as after Google sign-in, cannot be moved to
	// another address by linking one: that is an email change.
	if _, err := e.Pool.Exec(context.Background(), `DELETE FROM auth.user_passwords; DELETE FROM auth.user_providers WHERE provider_id = 'password'`); err != nil {
		t.Fatal(err)
	}
	authErr(t, p.call(itk+"accounts:signUp", map[string]any{"idToken": old, "email": "attacker@example.com", "password": "attacker1"}), "OPERATION_NOT_ALLOWED")
	u := ok(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": old}))
	assertEqual(t, "email kept", u.JSON["users"].([]any)[0].(map[string]any)["email"], "victim@example.com")
}

// H-2: a change-email code is the authority of the session that asked for it, so it ends with
// that session. Otherwise whoever squatted an address keeps a way back in after takeover
// protection drops their password, and a stolen session survives the password change that was
// meant to end it.
func TestAuthChangeEmailCodeEndsWithItsSession(t *testing.T) {
	x := newIdPEnv(t)
	squatter := ok(t, x.call(itk+"accounts:signUp", map[string]any{"email": "victim@gmail.com", "password": "squatter1", "returnSecureToken": true}))
	ok(t, x.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_AND_CHANGE_EMAIL", "idToken": squatter.str("idToken"), "newEmail": "attacker@evil.test"}))
	_, q := linkCode(t, x.lastMail("attacker@evil.test")["link"].(string))

	// The owner signs in with Google; takeover protection ends the squatter's sessions.
	res := ok(t, x.signIn(x.flow("google.com", "signInViaPopup", googleClaims("g-victim", "victim@gmail.com", true)), ""))
	assertEqual(t, "same account", res.str("localId"), squatter.str("localId"))
	authErr(t, x.call(itk+"accounts:update", map[string]any{"oobCode": q.Get("oobCode")}), "INVALID_OOB_CODE")
	user := ok(t, x.call(itk+"accounts:lookup", map[string]any{"idToken": res.str("idToken")})).list("users")[0]
	assertEqual(t, "email kept", user["email"], "victim@gmail.com")

	// A stolen session asks for a change; the owner changes the password, which revokes it.
	owner := ok(t, x.call(itk+"accounts:signUp", map[string]any{"email": "owner@example.com", "password": "secret12", "returnSecureToken": true}))
	ok(t, x.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_AND_CHANGE_EMAIL", "idToken": owner.str("idToken"), "newEmail": "thief@evil.test"}))
	_, q = linkCode(t, x.lastMail("thief@evil.test")["link"].(string))
	changed := ok(t, x.call(itk+"accounts:update", map[string]any{"idToken": owner.str("idToken"), "password": "secret34", "returnSecureToken": true}))
	authErr(t, x.call(itk+"accounts:update", map[string]any{"oobCode": q.Get("oobCode")}), "INVALID_OOB_CODE")
	// A change the owner asks for right after, with the session the password change returned,
	// works, even within the second of the revocation.
	ok(t, x.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_AND_CHANGE_EMAIL", "idToken": changed.str("idToken"), "newEmail": "new@example.com"}))
	_, q = linkCode(t, x.lastMail("new@example.com")["link"].(string))
	ok(t, x.call(itk+"accounts:update", map[string]any{"oobCode": q.Get("oobCode")}))
}

// H-3: a revocation ends every session started before it, also those started earlier in the
// same second. Otherwise whoever knows the password and signs in in a loop keeps a session
// through the password change meant to lock them out (or through takeover protection), while
// the session the change itself returns must go on working, as the web SDK takes it.
func TestAuthRevocationEndsSessionsOfTheSameSecond(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "owner@example.com", "password": "secret1"}))
	signIn := func(pw string) *Response {
		return ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "owner@example.com", "password": pw, "returnSecureToken": true}))
	}
	pw, next := "secret1", "secret2"
	for round := 0; ; round++ {
		if round == 20 {
			t.Fatal("no round fell within one second")
		}
		start := time.Now().Unix()
		thief := signIn(pw)
		owner := signIn(pw)
		upd := ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": owner.str("idToken"), "password": next, "returnSecureToken": true}))
		pw, next = next, pw
		if time.Now().Unix() != start {
			continue // the second ticked over; only a round within one second shows the case
		}
		authErr(t, p.refresh(thief.str("refreshToken")), "TOKEN_EXPIRED")
		authErr(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": thief.str("idToken")}), "TOKEN_EXPIRED")
		// The web SDK keeps its refresh token when the returned ID token is the one it holds,
		// so the change must return a different one, and that session must work.
		if upd.str("idToken") == owner.str("idToken") {
			t.Fatal("the password change returned the ID token of the revoked session")
		}
		ok(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": upd.str("idToken")}))
		ok(t, p.refresh(upd.str("refreshToken")))
		return
	}
}

// A second revocation in the same second also ends a session started after the first one:
// such a session is dated at the first revocation's point, so the second moves past it.
func TestAuthSecondRevocationInTheSameSecond(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "owner@example.com", "password": "secret1"}))
	// As if a revocation had just happened in this second: its point is the next second.
	p.sql(`UPDATE auth.users SET tokens_valid_after = date_trunc('second', now()) + interval '1 second'`)
	signIn := func() *Response {
		return ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "owner@example.com", "password": "secret1", "returnSecureToken": true}))
	}
	thief, owner := signIn(), signIn()
	upd := ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": owner.str("idToken"), "password": "secret2", "returnSecureToken": true}))
	authErr(t, p.refresh(thief.str("refreshToken")), "TOKEN_EXPIRED")
	ok(t, p.refresh(upd.str("refreshToken")))
}
