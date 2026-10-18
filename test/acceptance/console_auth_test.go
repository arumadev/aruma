package acceptance

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// The console's Auth routes: users, sign-in settings and signing keys of a project the caller
// owns (docs/api/console.md).

func consoleAuth(project string) string { return "/console/v1/projects/" + project + "/auth" }

func TestConsoleAuthUsers(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	base := consoleAuth("chat-app")

	created := p.owner.mustOK(p.owner.do("POST", base+"/users", map[string]any{
		"email": "sara@example.com", "password": "secret1", "displayName": "سارة", "emailVerified": true,
	}))
	uid := created.str("uid")
	if len(uid) != 28 || created.str("email") != "sara@example.com" || created.JSON["hasPassword"] != true || created.JSON["emailVerified"] != true {
		t.Fatalf("created user: %s", created.Body)
	}
	providers := created.list("providers")
	if len(providers) != 1 || providers[0]["providerId"] != "password" || providers[0]["email"] != "sara@example.com" {
		t.Fatalf("providers: %s", created.Body)
	}
	if strings.Contains(string(created.Body), "argon2") || strings.Contains(string(created.Body), "passwordHash") {
		t.Fatal("the console leaks the password hash")
	}
	expect(t, p.owner.do("POST", base+"/users", map[string]any{"email": "sara@example.com"}), 409, "ALREADY_EXISTS")
	weak := p.owner.do("POST", base+"/users", map[string]any{"email": "w@example.com", "password": "123"})
	expect(t, weak, 400, "INVALID_ARGUMENT")
	if !strings.HasPrefix(weak.str("error", "message"), "WEAK_PASSWORD") {
		t.Fatalf("the Identity Toolkit code must lead the message: %s", weak.Body)
	}
	expect(t, p.owner.do("POST", base+"/users", map[string]any{"email": "x@example.com", "passwordHash": "x"}), 400, "INVALID_ARGUMENT")

	// The new user signs in from the client SDK with the password set in the console.
	ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "secret1"}))

	p.owner.mustOK(p.owner.do("POST", base+"/users", map[string]any{"uid": "custom-uid-1"}))
	for i := range 3 {
		p.owner.mustOK(p.owner.do("POST", base+"/users", map[string]any{"email": fmt.Sprintf("team%d@example.com", i)}))
	}

	all := p.owner.mustOK(p.owner.do("GET", base+"/users", nil))
	if len(all.list("users")) != 5 || all.JSON["totalSize"] != float64(5) || all.str("nextPageToken") != "" {
		t.Fatalf("list: %s", all.Body)
	}
	// Pages follow the stable uid order.
	var seen []string
	token := ""
	for {
		page := p.owner.mustOK(p.owner.do("GET", base+"/users?pageSize=2&pageToken="+token, nil))
		for _, u := range page.list("users") {
			seen = append(seen, u["uid"].(string))
		}
		if token = page.str("nextPageToken"); token == "" {
			break
		}
	}
	if len(seen) != 5 {
		t.Fatalf("paged through %d users: %v", len(seen), seen)
	}

	// Search: an email prefix, case-insensitively, or an exact uid.
	byPrefix := p.owner.mustOK(p.owner.do("GET", base+"/users?query=TEAM", nil))
	if len(byPrefix.list("users")) != 3 || byPrefix.JSON["totalSize"] != float64(3) {
		t.Fatalf("prefix search: %s", byPrefix.Body)
	}
	byUID := p.owner.mustOK(p.owner.do("GET", base+"/users?query=custom-uid-1", nil))
	if users := byUID.list("users"); len(users) != 1 || users[0]["uid"] != "custom-uid-1" {
		t.Fatalf("uid search: %s", byUID.Body)
	}
	if n := len(p.owner.mustOK(p.owner.do("GET", base+"/users?query=custom-uid", nil)).list("users")); n != 0 {
		t.Fatalf("a uid matches exactly, got %d", n)
	}
	page := p.owner.mustOK(p.owner.do("GET", base+"/users?query=team&pageSize=2", nil))
	if len(page.list("users")) != 2 || page.str("nextPageToken") == "" || page.JSON["totalSize"] != float64(3) {
		t.Fatalf("search page: %s", page.Body)
	}

	got := p.owner.mustOK(p.owner.do("GET", base+"/users/"+uid, nil))
	assertEqual(t, "displayName", got.str("displayName"), "سارة")
	if got.str("createTime") == "" || got.str("lastSignInTime") == "" {
		t.Fatalf("metadata: %s", got.Body)
	}
	expect(t, p.owner.do("GET", base+"/users/nobody", nil), 404, "NOT_FOUND")
}

func TestConsoleAuthUserChanges(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	base := consoleAuth("chat-app")
	up := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "omar@example.com", "password": "secret1", "returnSecureToken": true}))
	user := base + "/users/" + up.str("localId")

	// Custom claims travel as a JSON string and appear in the next token, not the current one.
	claimed := p.owner.mustOK(p.owner.do("PATCH", user, map[string]any{"customClaims": `{"admin": true, "tier": "gold"}`}))
	var stored map[string]any
	if err := json.Unmarshal([]byte(claimed.str("customClaims")), &stored); err != nil || stored["admin"] != true || stored["tier"] != "gold" {
		t.Fatalf("customClaims: %s", claimed.Body)
	}
	if _, has := claims(t, up.str("idToken"))["admin"]; has {
		t.Fatal("claims must not reach a token issued before the change")
	}
	assertEqual(t, "claim after refresh", claims(t, ok(t, p.refresh(up.str("refreshToken"))).str("id_token"))["admin"], true)

	for claimsIn, code := range map[string]string{
		`{"sub": "x"}`: "FORBIDDEN_CLAIM",
		`[1, 2]`:       "INVALID_CLAIMS",
		`{"broken"`:    "INVALID_CLAIMS",
		`{"big": "` + strings.Repeat("x", 1000) + `"}`: "CLAIMS_TOO_LARGE",
	} {
		r := p.owner.do("PATCH", user, map[string]any{"customClaims": claimsIn})
		expect(t, r, 400, "INVALID_ARGUMENT")
		if !strings.HasPrefix(r.str("error", "message"), code) {
			t.Errorf("claims %.20q: want %s, got %s", claimsIn, code, r.str("error", "message"))
		}
	}
	cleared := p.owner.mustOK(p.owner.do("PATCH", user, map[string]any{"customClaims": ""}))
	if _, has := cleared.JSON["customClaims"]; has {
		t.Fatalf("claims not cleared: %s", cleared.Body)
	}

	// Disable, then enable again (AC-A12).
	p.owner.mustOK(p.owner.do("PATCH", user, map[string]any{"disabled": true}))
	authErr(t, p.refresh(up.str("refreshToken")), "USER_DISABLED")
	p.owner.mustOK(p.owner.do("PATCH", user, map[string]any{"disabled": false}))
	ok(t, p.refresh(up.str("refreshToken")))

	// Revoking sessions ends every refresh token issued so far.
	revoked := p.owner.mustOK(p.owner.do("POST", user+":revokeSessions", nil))
	if revoked.str("tokensValidAfterTime") == "" {
		t.Fatalf("revoke: %s", revoked.Body)
	}
	authErr(t, p.refresh(up.str("refreshToken")), "TOKEN_EXPIRED")

	// A new password from the console works and the old one does not.
	p.owner.mustOK(p.owner.do("PATCH", user, map[string]any{"password": "secret2", "displayName": "Omar"}))
	ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "omar@example.com", "password": "secret2"}))
	authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "omar@example.com", "password": "secret1"}), "INVALID_LOGIN_CREDENTIALS")

	expect(t, p.owner.do("DELETE", user, nil), 204, "")
	expect(t, p.owner.do("GET", user, nil), 404, "NOT_FOUND")
	expect(t, p.owner.do("DELETE", user, nil), 404, "NOT_FOUND")
	authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "omar@example.com", "password": "secret2"}), "INVALID_LOGIN_CREDENTIALS")
}

func TestConsoleAuthConfig(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	base := consoleAuth("chat-app")

	cfg := p.owner.mustOK(p.owner.do("GET", base+"/config", nil))
	if cfg.JSON["passwordEnabled"] != true || cfg.JSON["anonymousEnabled"] != false || cfg.JSON["enumerationProtection"] != true ||
		cfg.JSON["clientSignupEnabled"] != true || cfg.JSON["passwordMinLength"] != float64(6) || cfg.str("passwordEnforcement") != "OFF" {
		t.Fatalf("defaults: %s", cfg.Body)
	}
	limits, _ := cfg.JSON["limits"].(map[string]any)
	if limits["idTokenLifetimeSeconds"] != float64(3600) || limits["signingKeyRotationDays"] != float64(30) || limits["passwordFailures"] != float64(10) {
		t.Fatalf("limits: %s", cfg.Body)
	}

	authErr(t, p.call(itk+"accounts:signUp", map[string]any{}), "OPERATION_NOT_ALLOWED")
	upd := p.owner.mustOK(p.owner.do("PATCH", base+"/config", map[string]any{
		"anonymousEnabled": true, "authorizedDomains": []string{"localhost", " Chat.Example.com ", "chat.example.com", "127.0.0.1"},
		"passwordMinLength": 8, "appName": "دردشة",
	}))
	assertEqual(t, "domains", fmt.Sprint(upd.JSON["authorizedDomains"]), "[localhost chat.example.com 127.0.0.1]")
	assertEqual(t, "unchanged field", upd.JSON["enumerationProtection"], true)
	ok(t, p.call(itk+"accounts:signUp", map[string]any{}))
	authErr(t, p.call(itk+"accounts:signUp", map[string]any{"email": "short@example.com", "password": "secret1"}), "WEAK_PASSWORD")

	for _, body := range []map[string]any{
		{"authorizedDomains": []string{"https://chat.example.com"}},
		{"authorizedDomains": []string{"*.example.com"}},
		{"authorizedDomains": []string{"chat.example.com:8080"}},
		{"authorizedDomains": []string{"example.com/path"}},
		{"passwordMinLength": 5},
		{"passwordEnforcement": "SOMETIMES"},
		{"unknownField": true},
	} {
		expect(t, p.owner.do("PATCH", base+"/config", body), 400, "INVALID_ARGUMENT")
	}
	assertEqual(t, "after refusals", p.owner.mustOK(p.owner.do("GET", base+"/config", nil)).JSON["passwordMinLength"], float64(8))
}

// AC-A15 from the console: a normal rotation keeps issued tokens valid, an emergency one
// invalidates them at once.
func TestConsoleAuthSigningKeys(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	base := consoleAuth("chat-app")

	if keys := p.owner.mustOK(p.owner.do("GET", base+"/signingKeys", nil)).list("keys"); len(keys) != 0 {
		t.Fatalf("a project that never issued a token has no keys: %v", keys)
	}
	up := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "k@example.com", "password": "secret1", "returnSecureToken": true}))
	states := func(r *Response) map[string]string {
		m := map[string]string{}
		for _, k := range r.list("keys") {
			if k["purpose"] == "id_token" {
				m[k["kid"].(string)] = k["state"].(string)
			}
		}
		return m
	}
	before := p.owner.mustOK(p.owner.do("GET", base+"/signingKeys", nil))
	if s := states(before); len(s) != 2 {
		t.Fatalf("want active and next id_token keys: %s", before.Body)
	}
	for _, leak := range []string{`"d"`, "private", "PRIVATE", `"n"`} {
		if strings.Contains(string(before.Body), leak) {
			t.Fatalf("key listing exposes %s: %s", leak, before.Body)
		}
	}
	activeKid := ""
	for kid, st := range states(before) {
		if st == "active" {
			activeKid = kid
		}
	}

	rotated := p.owner.mustOK(p.owner.do("POST", base+"/signingKeys:rotate", map[string]any{"purpose": "id_token"}))
	assertEqual(t, "old key after rotation", states(rotated)[activeKid], "retired")
	ok(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": up.str("idToken")}))

	fresh := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "k@example.com", "password": "secret1"}))
	p.owner.mustOK(p.owner.do("POST", base+"/signingKeys:rotate", map[string]any{"purpose": "id_token", "emergency": true}))
	authErr(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": fresh.str("idToken")}), "INVALID_ID_TOKEN")
	ok(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": ok(t, p.refresh(fresh.str("refreshToken"))).str("id_token")}))

	expect(t, p.owner.do("POST", base+"/signingKeys:rotate", map[string]any{"purpose": "access_token"}), 400, "INVALID_ARGUMENT")
}

// Another account sees none of this project's Auth data and changes nothing; a CLI token of
// the owner works like the session.
func TestConsoleAuthOwnership(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	base := consoleAuth("chat-app")
	up := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "mine@example.com", "password": "secret1"}))
	user := base + "/users/" + up.str("localId")

	other := e.account("other@example.com")
	other.project("other-app")
	for _, rt := range []struct {
		method, path string
		body         any
	}{
		{"GET", base + "/users", nil},
		{"GET", base + "/users?query=mine", nil},
		{"POST", base + "/users", map[string]any{"email": "intruder@example.com"}},
		{"GET", user, nil},
		{"PATCH", user, map[string]any{"disabled": true}},
		{"POST", user + ":revokeSessions", nil},
		{"DELETE", user, nil},
		{"GET", base + "/config", nil},
		{"PATCH", base + "/config", map[string]any{"anonymousEnabled": true}},
		{"GET", base + "/signingKeys", nil},
		{"POST", base + "/signingKeys:rotate", map[string]any{"purpose": "id_token", "emergency": true}},
	} {
		expect(t, other.do(rt.method, rt.path, rt.body), 404, "NOT_FOUND")
	}
	// The other account's own project does not reach into this one by uid.
	expect(t, other.do("GET", consoleAuth("other-app")+"/users/"+up.str("localId"), nil), 404, "NOT_FOUND")

	got := p.owner.mustOK(p.owner.do("GET", user, nil))
	if got.JSON["disabled"] != false {
		t.Fatalf("changed by another account: %s", got.Body)
	}
	if n := len(p.owner.mustOK(p.owner.do("GET", base+"/users", nil)).list("users")); n != 1 {
		t.Fatalf("users after the other account's attempts: %d", n)
	}
	assertEqual(t, "anonymous", p.owner.mustOK(p.owner.do("GET", base+"/config", nil)).JSON["anonymousEnabled"], false)
	ok(t, p.refresh(up.str("refreshToken")))

	cli := e.withBearer(p.owner.pat())
	if n := len(cli.mustOK(cli.do("GET", base+"/users", nil)).list("users")); n != 1 {
		t.Fatalf("CLI token listing: %d", n)
	}
}
