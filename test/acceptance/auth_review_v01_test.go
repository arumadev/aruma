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

// S-3: a recovery link undoes the change it was sent for, and only while that change is the
// account's latest: after A→B→C the link sent to A cannot bring A back, nor can it once the
// link sent to B has restored B. A change of address retires the codes of the old address, and
// a change of password the open reset links.
func TestAuthRecoverCodeUndoesOnlyTheLatestChange(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := p.signUp("a@example.com", "secret1")
	code := func(to, kind string) string {
		t.Helper()
		m := p.lastMail(to)
		assertEqual(t, "mail type", m["type"], kind)
		_, q := linkCode(t, m["link"].(string))
		return q.Get("oobCode")
	}
	apply := func(c string) *Response {
		t.Helper()
		return p.call(itk+"accounts:update", map[string]any{"oobCode": c})
	}
	change := func(idToken, to string) *Response {
		t.Helper()
		p.ageSessions()
		ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_AND_CHANGE_EMAIL", "idToken": idToken, "newEmail": to}))
		ok(t, apply(code(to, "VERIFY_AND_CHANGE_EMAIL")))
		return ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": to, "password": "secret1", "returnSecureToken": true}))
	}

	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "a@example.com"}))
	resetA := code("a@example.com", "PASSWORD_RESET")
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_EMAIL", "idToken": up.str("idToken")}))
	verifyA := code("a@example.com", "VERIFY_EMAIL")

	b := change(up.str("idToken"), "b@example.com")
	recoverA := code("a@example.com", "RECOVER_EMAIL")
	c := change(b.str("idToken"), "c@example.com")
	recoverB := code("b@example.com", "RECOVER_EMAIL")
	p.ageSessions()
	ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": c.str("idToken"), "password": "secret2"}))

	// The link sent to A is for the change A→B, which is no longer the latest.
	authErr(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": recoverA}), "INVALID_OOB_CODE")
	authErr(t, apply(recoverA), "INVALID_OOB_CODE")
	// The link sent to B undoes B→C.
	restored := ok(t, apply(recoverB))
	assertEqual(t, "restored", restored.str("email"), "b@example.com")
	// The account is at B again, but A's link was retired by the change it lost to.
	authErr(t, apply(recoverA), "INVALID_OOB_CODE")
	// So were the old address's other links, which would otherwise wake up if A came back.
	authErr(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": resetA, "newPassword": "attacker1"}), "INVALID_OOB_CODE")
	authErr(t, apply(verifyA), "INVALID_OOB_CODE")
	var open int
	if err := e.Pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.oob_codes WHERE used_at IS NULL`).Scan(&open); err != nil || open != 0 {
		t.Fatalf("open codes after the recovery: %d %v", open, err)
	}

	// A password change retires the reset links that were open before it.
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "b@example.com"}))
	resetB := code("b@example.com", "PASSWORD_RESET")
	p.ageSessions()
	in := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "b@example.com", "password": "secret2", "returnSecureToken": true}))
	ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": in.str("idToken"), "password": "secret3"}))
	authErr(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": resetB, "newPassword": "attacker1"}), "INVALID_OOB_CODE")
	ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "b@example.com", "password": "secret3"}))
}
