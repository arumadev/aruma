package acceptance

import (
	"context"
	"testing"
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
