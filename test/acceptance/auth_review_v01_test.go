package acceptance

import (
	"context"
	"strings"
	"testing"
)

// Regression tests for the v0.1 security review (docs/security/review-v0.1.md).

// S-2: Google vouches for an address only where it is the mail host: @gmail.com, or a Google
// Workspace account whose hosted domain (hd) is the address's domain. Anyone can open a Google
// account under another company's address, so for other domains the sign-in must not merge into
// the account that owns the address, verified or not, and must not delete its password.
func TestAuthGoogleEmailTrustedOnlyWhereGoogleHostsIt(t *testing.T) {
	x := newIdPEnv(t)
	signUp := func(email string, verified bool) string {
		t.Helper()
		uid := ok(t, x.call(itk+"accounts:signUp", map[string]any{"email": email, "password": "secret12", "returnSecureToken": true})).str("localId")
		if verified {
			x.sql(`UPDATE auth.users SET email_verified = true WHERE uid = $1`, uid)
		}
		return uid
	}
	google := func(sub, email string, extra map[string]any) *Response {
		t.Helper()
		c := googleClaims(sub, email, true)
		for k, v := range extra {
			c[k] = v
		}
		return x.direct("google.com", x.google.idToken(googleClient, c), "", "")
	}
	needsConfirmation := func(res *Response) {
		t.Helper()
		ok(t, res)
		if res.JSON["needConfirmation"] != true || res.str("idToken") != "" || res.str("localId") != "" || res.str("oauthIdToken") == "" {
			t.Fatalf("want needConfirmation without a session: %s", res.Body)
		}
	}
	untouched := func(email, uid string) {
		t.Helper()
		in := ok(t, x.call(itk+"accounts:signInWithPassword", map[string]any{"email": email, "password": "secret12", "returnSecureToken": true}))
		assertEqual(t, "uid", in.str("localId"), uid)
		user := ok(t, x.call(itk+"accounts:lookup", map[string]any{"idToken": in.str("idToken")})).list("users")[0]
		if infos := user["providerUserInfo"].([]any); len(infos) != 1 || infos[0].(map[string]any)["providerId"] != "password" {
			t.Fatalf("providers of %s changed: %v", email, infos)
		}
	}

	// A verified password account, and a Google account someone else opened with its address.
	alice := signUp("alice@corp.example", true)
	needsConfirmation(google("g-attacker", "alice@corp.example", nil))
	untouched("alice@corp.example", alice)

	// An unverified one: the password stays too, as Google does not vouch for this address.
	bob := signUp("bob@corp.example", false)
	needsConfirmation(google("g-attacker-2", "bob@corp.example", nil))
	untouched("bob@corp.example", bob)

	// A hosted domain that is not the address's domain does not vouch for it either.
	needsConfirmation(google("g-attacker-3", "alice@corp.example", map[string]any{"hd": "evil.example"}))
	untouched("alice@corp.example", alice)

	// A new user under another domain gets an account whose address is not verified.
	res := ok(t, google("g-new", "new@corp.example", nil))
	assertEqual(t, "emailVerified", res.JSON["emailVerified"], false)
	assertEqual(t, "isNewUser", res.JSON["isNewUser"], true)
	assertEqual(t, "email_verified claim", claims(t, res.str("idToken"))["email_verified"], false)
	if !strings.Contains(res.str("rawUserInfo"), `"verified_email":true`) {
		t.Fatalf("rawUserInfo should keep what Google said: %s", res.str("rawUserInfo"))
	}

	// A Workspace account of the domain is authoritative for it, as is Gmail (also googlemail.com).
	res = ok(t, google("g-alice", "alice@corp.example", map[string]any{"hd": "CORP.example"}))
	assertEqual(t, "workspace account merges", res.str("localId"), alice)
	assertEqual(t, "emailVerified", res.JSON["emailVerified"], true)
	gmail := signUp("carol@googlemail.com", true)
	assertEqual(t, "googlemail merges", ok(t, google("g-carol", "carol@googlemail.com", nil)).str("localId"), gmail)

	var verified bool
	if err := x.env.Pool.QueryRow(context.Background(), `SELECT email_verified FROM auth.users WHERE email = 'bob@corp.example'`).Scan(&verified); err != nil || verified {
		t.Fatalf("bob verified=%v err=%v", verified, err)
	}
}
