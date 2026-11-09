package acceptance

import (
	"testing"
	"time"
)

// Hardening: a credential stops working everywhere, at once, when what stands behind it is
// disabled, deleted or revoked. Each case checks the credential on every service before and
// after, so a service that cached the answer would show.

// newHardUser makes a user of the project and returns an ID token and the refresh token.
func (h *hardEnv) newHardUser(p *hardProject, uid string) (idToken, refresh string) {
	h.t.Helper()
	p.users.create(p.id, uid)
	in := p.users.signIn(p.id, uid)
	return in.str("idToken"), in.str("refreshToken")
}

func TestHardUserLifecycleEndsSessionsOnEveryService(t *testing.T) {
	h := newHardEnv(t, nil)
	h.open(h.b, "bravo-secret")
	p := h.b
	ap := &authProject{env: h.Env, owner: p.owner, id: p.id, number: p.number, key: p.key}
	ap.ageSessions() // revocation works at second precision: start the cases a clear second later

	for _, c := range []struct {
		name string
		act  func(uid string)
	}{
		{"disabled through the Admin API", func(uid string) {
			p.users.admin(p.id, "accounts:update", map[string]any{"localId": uid, "disableUser": true})
		}},
		{"deleted through the Admin API", func(uid string) {
			p.users.admin(p.id, "accounts:delete", map[string]any{"localId": uid})
		}},
		{"sessions revoked from the console", func(uid string) {
			p.owner.mustOK(p.owner.do("POST", consoleAuth(p.id)+"/users/"+uid+":revokeSessions", nil))
		}},
		{"password changed through the Admin API", func(uid string) {
			p.users.admin(p.id, "accounts:update", map[string]any{"localId": uid, "password": "changed-" + uid})
		}},
		{"disabled from the console", func(uid string) {
			p.owner.mustOK(p.owner.do("PATCH", consoleAuth(p.id)+"/users/"+uid, map[string]any{"disabled": true}))
		}},
		{"deleted from the console", func(uid string) {
			p.owner.mustOK(p.owner.do("DELETE", consoleAuth(p.id)+"/users/"+uid, nil))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			uid := "u-" + string(rune('a'+len(c.name)%26)) + itoa(len(c.name))
			idToken, refresh := h.newHardUser(p, uid)
			ap.ageSessions()
			time.Sleep(10 * time.Millisecond)
			surfaces := hardSurfaces()
			for _, s := range surfaces {
				if got := s.read(h, p, idToken); got != p.marker {
					t.Fatalf("%s before: served %q", s.name, got)
				}
			}
			// A WebSocket that authenticated before the change.
			ws := h.dial(p.ns, dialOpts{})
			ws.call("auth", map[string]any{"cred": idToken}).mustOK(t)
			if got := ws.get("/secret"); got != `"`+p.marker+`"` {
				t.Fatalf("websocket before: %s", got)
			}

			c.act(uid)

			for _, s := range surfaces {
				if got := s.read(h, p, idToken); got != "" {
					t.Errorf("%s: the token still served %q after the user was %s", s.name, got, c.name)
				}
			}
			if r := ap.refresh(refresh); r.Status == 200 {
				t.Errorf("the refresh token still works after the user was %s: %s", c.name, r.Body)
			}
			// The open connection loses the user's access: either the server drops it or the read
			// is denied.
			if r := ws.call("g", map[string]any{"p": "/secret", "q": map[string]any{}}); r.Status == "ok" {
				t.Errorf("the open WebSocket still reads after the user was %s: %s", c.name, r.Data)
			}
		})
	}
}

// Service account credentials: deleting the key or the account ends the access token and the
// self-signed JWT the Admin SDKs send to the Document Database; deleting the project ends all
// of them and the owner's token too.
func TestHardServiceAccountLifecycleEndsAccessEverywhere(t *testing.T) {
	h := newHardEnv(t, nil)
	h.open(h.b, "bravo-secret")
	p := h.b
	jwt := func(key []byte, mut func(map[string]any)) string {
		k := parseKeyFile(t, key)
		now := time.Now().Unix()
		c := map[string]any{"iss": k.ClientEmail, "sub": k.ClientEmail, "scope": "https://www.googleapis.com/auth/datastore", "iat": now, "exp": now + 3600, "aud": "https://firestore.googleapis.com/"}
		if mut != nil {
			mut(c)
		}
		return signedAssertion(t, k, map[string]any{"alg": "RS256", "typ": "JWT", "kid": k.PrivateKeyID}, c)
	}
	docs := func(tok string) bool {
		code, _ := documentsCall(t, h.Env, tok, "GET", hardDocPath(p.id), "")
		return code == 200
	}
	// The self-signed JWT is an admin of the project on the Document Database, and nowhere else.
	good := jwt(p.saKey, nil)
	if !docs(good) {
		t.Fatal("control: the self-signed JWT is refused")
	}
	for name, mut := range map[string]func(map[string]any){
		"aud is the token endpoint": func(c map[string]any) { c["aud"] = h.URL + "/oauth2/v1/token" },
		"aud is another host":       func(c map[string]any) { c["aud"] = "https://example.com/" },
		"no scope":                  func(c map[string]any) { delete(c, "scope") },
		"scope is not a database":   func(c map[string]any) { c["scope"] = "email profile" },
		"expired":                   func(c map[string]any) { c["iat"], c["exp"] = time.Now().Unix()-7200, time.Now().Unix()-3600 },
		"lives two hours":           func(c map[string]any) { c["exp"] = time.Now().Unix() + 7200 },
		"sub differs from iss":      func(c map[string]any) { c["sub"] = "someone@" + p.id + ".sa.127.0.0.1" },
		"issued in the future":      func(c map[string]any) { c["iat"], c["exp"] = time.Now().Unix()+600, time.Now().Unix()+1200 },
	} {
		if docs(jwt(p.saKey, mut)) {
			t.Errorf("self-signed JWT with %s was accepted", name)
		}
	}
	// A JWT of the other project's service account, and one signed by a key the account lacks.
	if docs(jwt(h.a.saKey, nil)) {
		t.Error("project A's service account JWT was accepted by project B")
	}
	_, other := p.owner.serviceAccountKey(p.id, "second")
	forged := parseKeyFile(t, other)
	own := parseKeyFile(t, p.saKey)
	forged.ClientEmail = own.ClientEmail // another account's key, claiming this account's name
	if docs(signedAssertion(t, forged, map[string]any{"alg": "RS256", "typ": "JWT"}, map[string]any{
		"iss": own.ClientEmail, "sub": own.ClientEmail, "scope": "https://www.googleapis.com/auth/datastore",
		"iat": time.Now().Unix(), "exp": time.Now().Unix() + 3600})) {
		t.Error("a JWT signed by another account's key was accepted")
	}
	// The JWT is a Document Database credential only: every other API refuses it as a bearer.
	for _, s := range hardAdminSurfaces() {
		if s.name == "documents rest" {
			continue
		}
		if got := s.read(h, p, good); got != "" {
			t.Errorf("the Document Database JWT served %q on %s", got, s.name)
		}
	}

	// Delete the key: the token and the JWT both stop, on every surface.
	k := parseKeyFile(t, p.saKey)
	p.owner.mustOK(p.owner.do("DELETE", "/console/v1/projects/"+p.id+"/serviceAccounts/"+p.saID+"/keys/"+k.PrivateKeyID, nil))
	for _, s := range hardAdminSurfaces() {
		if got := s.read(h, p, p.sat); got != "" && s.sat {
			t.Errorf("%s: the access token still served after its key was deleted", s.name)
		}
	}
	if docs(good) {
		t.Error("the self-signed JWT still works after its key was deleted")
	}
	if _, _, err := exchange(t, p.saKey); err == nil {
		t.Error("the deleted key still exchanges")
	}
}

// An owner's CLI token stops with its revocation, with a password change and with the
// project's deletion, on every service.
func TestHardOwnerTokenLifecycle(t *testing.T) {
	h := newHardEnv(t, nil)
	h.open(h.b, "bravo-secret")
	p := h.b
	pat := func(c *Client, name, password string) (id, tok string) {
		r := c.mustOK(c.do("POST", "/console/v1/me/tokens", map[string]any{"name": name, "currentPassword": password}))
		return r.str("id"), r.str("token")
	}
	served := func(tok string) []string {
		var bad []string
		for _, s := range hardAdminSurfaces() {
			if s.pat && s.read(h, p, tok) == p.marker {
				bad = append(bad, s.name)
			}
		}
		return bad
	}
	id, tok := pat(p.owner, "revoked", testPassword)
	if got := served(tok); len(got) == 0 {
		t.Fatal("control: the CLI token serves nothing")
	}
	p.owner.mustOK(p.owner.do("DELETE", "/console/v1/me/tokens/"+id, nil))
	if got := served(tok); len(got) != 0 {
		t.Errorf("a revoked CLI token still served %v", got)
	}

	_, tok = pat(p.owner, "password", testPassword)
	p.owner.mustOK(p.owner.do("POST", "/console/v1/me:changePassword", map[string]any{"currentPassword": testPassword, "newPassword": "another correct horse"}))
	if got := served(tok); len(got) != 0 {
		t.Errorf("a CLI token survived its owner's password change on %v", got)
	}

	c := h.client()
	c.login("b@example.com", "another correct horse")
	_, tok = pat(c, "project-deleted", "another correct horse")
	c.mustOK(c.do("POST", "/console/v1/projects/"+p.id+":delete", map[string]any{"confirmProjectId": p.id}))
	for _, s := range hardAdminSurfaces() {
		if s.name == "console api" {
			continue // the owner still reaches the project's console for the 30 days it can be restored
		}
		for what, token := range map[string]string{"CLI token": tok, "service account token": p.sat, "ID token": ""} {
			if token == "" || (what == "CLI token" && !s.pat) || (what == "service account token" && !s.sat) {
				continue
			}
			if got := s.read(h, p, token); got != "" {
				t.Errorf("%s: a %s still served %q after the project was deleted", s.name, what, got)
			}
		}
	}
	for _, s := range hardSurfaces() {
		if got := s.read(h, p, p.idToken); got != "" {
			t.Errorf("%s: a user's ID token still served %q after the project was deleted", s.name, got)
		}
	}
}

// Expiry is enforced on the credential's own clock, on every service: an access token past its
// hour, a CLI token past its expiry and a console session past its idle or absolute limit are
// refused wherever they were taken before.
func TestHardExpiredCredentialsAreRefusedEverywhere(t *testing.T) {
	h := newHardEnv(t, nil)
	h.open(h.b, "bravo-secret")
	p := h.b
	exp := func(what string, tok string, stmt string) {
		t.Helper()
		var served []string
		for _, s := range hardAdminSurfaces() {
			if got := s.read(h, p, tok); got == p.marker {
				served = append(served, s.name)
			}
		}
		if len(served) == 0 {
			t.Fatalf("control: the %s serves nothing", what)
		}
		if _, err := h.Pool.Exec(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
		for _, s := range hardAdminSurfaces() {
			if got := s.read(h, p, tok); got != "" {
				t.Errorf("%s: the expired %s still served %q", s.name, what, got)
			}
		}
	}
	exp("service account token", p.sat, `UPDATE control.oauth_access_tokens SET expires_at = now() - interval '1 second'`)
	exp("CLI token", p.pat, `UPDATE control.access_tokens SET expires_at = now() - interval '1 second'`)

	// The console session: idle, then absolute, then revoked.
	for _, stmt := range []string{
		`UPDATE control.sessions SET idle_expires_at = now() - interval '1 second' WHERE true`,
		`UPDATE control.sessions SET idle_expires_at = now() + interval '1 hour', absolute_expires_at = now() - interval '1 second' WHERE true`,
	} {
		c := h.client()
		c.login("b@example.com", testPassword)
		c.mustOK(c.do("GET", "/console/v1/projects/"+p.id, nil))
		if _, err := h.Pool.Exec(t.Context(), stmt); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/console/v1/me", "/console/v1/projects/" + p.id, "/console/v1/projects/" + p.id + "/auth/users", "/console/v1/projects/" + p.id + "/storage/objects"} {
			expect(t, c.do("GET", path, nil), 401, "UNAUTHENTICATED")
		}
		expect(t, c.do("PATCH", "/console/v1/projects/"+p.id, map[string]any{"displayName": "late"}), 401, "UNAUTHENTICATED")
	}
}
