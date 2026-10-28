package acceptance

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Regression tests for the v0.1 security review.

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

// S-4: a display name is whatever the account holder typed, so a mail to an address the account
// has not proven (verification, a change of address, a reset or recovery for an unverified one)
// would carry a stranger's text from the project to any inbox. The built-in templates greet
// without the name, and a custom template's %DISPLAY_NAME% is empty in such mail.
func TestAuthMailCarriesNoNameToUnprovenAddresses(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	const lure = "Your bank account is locked, call +1 555 0100"
	up := p.signUp("victim@example.com", "secret1")
	ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": up.str("idToken"), "displayName": lure}))
	noLure := func(to, kind string) {
		t.Helper()
		m := p.lastMail(to)
		assertEqual(t, "type", m["type"], kind)
		for _, part := range []string{"subject", "text", "html"} {
			if strings.Contains(m[part].(string), "bank account") {
				t.Fatalf("%s mail to %s carries the display name in its %s: %v", kind, to, part, m[part])
			}
		}
	}
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_EMAIL", "idToken": up.str("idToken")}))
	noLure("victim@example.com", "VERIFY_EMAIL")
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "victim@example.com"}))
	noLure("victim@example.com", "PASSWORD_RESET")
	p.ageSessions()
	in := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "victim@example.com", "password": "secret1", "returnSecureToken": true}))
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_AND_CHANGE_EMAIL", "idToken": in.str("idToken"), "newEmail": "other@example.com"}))
	noLure("other@example.com", "VERIFY_AND_CHANGE_EMAIL")
	_, q := linkCode(t, p.lastMail("other@example.com")["link"].(string))
	ok(t, p.call(itk+"accounts:update", map[string]any{"oobCode": q.Get("oobCode")}))
	noLure("victim@example.com", "RECOVER_EMAIL") // the old address was never verified either

	// A custom template may greet by name; the name goes only to an address the account proved.
	for _, kind := range []string{"VERIFY_EMAIL", "PASSWORD_RESET"} {
		p.owner.mustOK(p.owner.do("PUT", "/console/v1/projects/chat-app/auth/templates/"+kind+"/en", map[string]any{
			"subject": "Hi %DISPLAY_NAME%", "body": `<p>Hello %DISPLAY_NAME%</p><p><a href="%LINK%">open</a></p>`,
		}))
	}
	in = ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "other@example.com", "password": "secret1", "returnSecureToken": true}))
	ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": in.str("idToken"), "displayName": "Sara"}))
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "other@example.com"}))
	assertEqual(t, "verified address", p.lastMail("other@example.com")["subject"], "Hi Sara")
	other := p.signUp("new@example.com", "secret1")
	ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": other.str("idToken"), "displayName": lure}))
	ok(t, p.call(itk+"accounts:sendOobCode", map[string]any{"requestType": "VERIFY_EMAIL", "idToken": other.str("idToken")}))
	assertEqual(t, "unverified address", p.lastMail("new@example.com")["subject"], "Hi")
}

// S-5: a project's SMTP settings make the server open TCP connections where the console user
// says, and smtp:test reported the raw answer: a port scanner and banner reader for the
// operator's network and the cloud metadata service. Loopback, private, link-local and similar
// hosts are refused unless the instance allows them (mail.smtp_allow_private), checked on the
// address actually dialled; the console gets a generic error and the log the detail.
func TestAuthSMTPReachesOnlyPublicHosts(t *testing.T) {
	e := newEnv(t, map[string]string{"ARUMA_MAIL_SMTP_ALLOW_PRIVATE": "false"})
	p := e.authProject("chat-app")
	smtp := "/console/v1/projects/chat-app/auth/smtp"
	for _, host := range []string{
		"127.0.0.1", "localhost", "LOCALHOST.", "mail.localhost", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254",
		"100.100.100.200", "0.0.0.0", "::1", "[::1]", "fe80::1", "fd00:ec2::254", "::ffff:127.0.0.1", "::",
	} {
		r := p.owner.do("PATCH", smtp, map[string]any{"host": host, "port": 25})
		expect(t, r, 400, "INVALID_ARGUMENT")
		if !strings.Contains(string(r.Body), "SMTP_HOST_PRIVATE") || !strings.Contains(string(r.Body), "smtp_allow_private") {
			t.Fatalf("%s: %s", host, r.Body)
		}
	}
	p.owner.mustOK(p.owner.do("PATCH", smtp, map[string]any{"host": "smtp.example.com", "port": 587}))
	p.owner.mustOK(p.owner.do("PATCH", smtp, map[string]any{"host": "93.184.216.34"}))

	// A name is checked where it leads, at the moment of the connection: a loopback name the
	// settings check cannot know is refused when dialled.
	ln := banner(t, "+OK Dovecot ready, internal-mail-7\r\n")
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	portN, _ := strconv.Atoi(port)
	p.owner.mustOK(p.owner.do("PATCH", smtp, map[string]any{"host": loopbackName(t), "port": portN, "tls": "none"}))
	r := p.owner.do("POST", smtp+":test", map[string]any{"to": "owner@example.com"})
	expect(t, r, 400, "INVALID_ARGUMENT")
	if !strings.Contains(string(r.Body), "SMTP_HOST_PRIVATE") || !strings.Contains(string(r.Body), "smtp_allow_private") {
		t.Fatalf("private name: %s", r.Body)
	}

	// Where private hosts are allowed (a local relay), the console still gets no banner.
	e = newEnv(t, map[string]string{"ARUMA_MAIL_SMTP_ALLOW_PRIVATE": "true"})
	p = e.authProject("chat-app")
	p.owner.mustOK(p.owner.do("PATCH", smtp, map[string]any{"host": "127.0.0.1", "port": portN, "tls": "none"}))
	r = p.owner.do("POST", smtp+":test", map[string]any{"to": "owner@example.com"})
	expect(t, r, 400, "INVALID_ARGUMENT")
	if strings.Contains(string(r.Body), "Dovecot") || strings.Contains(string(r.Body), port) || !strings.Contains(string(r.Body), "SMTP_ERROR") {
		t.Fatalf("smtp:test leaks the server's answer: %s", r.Body)
	}
	if !strings.Contains(e.Logs.String(), "Dovecot") {
		t.Fatal("the detail is not in the server log")
	}
}

// banner listens on loopback and greets every connection with line.
// loopbackName returns a host name, not "localhost", that this machine resolves to loopback:
// 127.1 on macOS, ip6-localhost from /etc/hosts on Debian and Ubuntu.
func loopbackName(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"127.1", "ip6-localhost", "localhost.localdomain"} {
		addrs, err := net.LookupHost(name)
		if err != nil || len(addrs) == 0 {
			continue
		}
		if ip := net.ParseIP(addrs[0]); ip != nil && ip.IsLoopback() {
			return name
		}
	}
	t.Skip("no loopback name other than localhost resolves here")
	return ""
}

func banner(t *testing.T, line string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte(line))
			time.AfterFunc(5*time.Second, func() { c.Close() })
		}
	}()
	return ln
}

// Info: removing a user's email through the Admin API left its password sign-in behind, keyed
// by the old address, so nobody could register that address again. A password needs an
// email: the removal is refused unless the password goes in the same call.
func TestAuthAdminEmailRemovalLeavesNoPasswordBehind(t *testing.T) {
	e := newEnv(t, nil)
	p := e.adminProject("chat-app")
	ok(t, p.admin(p.sa, "accounts", map[string]any{"localId": "user-1", "email": "sara@example.com", "password": "secret1"}))

	// Removing the email frees the address: the password provider goes with it.
	upd := ok(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": "user-1", "email": ""}))
	if upd.str("email") != "" || upd.JSON["providerUserInfo"] != nil {
		t.Fatalf("after removal: %s", upd.Body)
	}
	authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "secret1"}), "INVALID_LOGIN_CREDENTIALS")
	again := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "sara@example.com", "password": "secret2"}))
	if again.str("localId") == "user-1" {
		t.Fatal("the address came back to the old user")
	}

	// The password survives without an email and signs in again once the user has one.
	ok(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": "user-1", "email": "sara.new@example.com"}))
	in := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara.new@example.com", "password": "secret1"}))
	if in.str("localId") != "user-1" {
		t.Fatalf("sign-in after a new email: %s", in.Body)
	}
}
