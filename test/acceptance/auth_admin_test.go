package acceptance

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	identity "github.com/osama300/aurma-project/internal/identity/core"
)

// adminProject is an Auth project seen from a developer's server: the client view plus a
// service account token for the Admin API.
type adminProject struct {
	*authProject
	keyFile []byte
	sa      *Client // bearer: the service account's access token
}

func (e *Env) adminProject(id string) *adminProject {
	e.t.Helper()
	p := e.authProject(id)
	_, key := p.owner.serviceAccountKey(id, "backend")
	token, _, err := exchange(e.t, key)
	if err != nil {
		e.t.Fatal(err)
	}
	return &adminProject{authProject: p, keyFile: key, sa: e.withBearer(token)}
}

// admin posts an Admin API call, as firebase-admin does, with the given caller.
func (p *adminProject) admin(c *Client, call string, body any) *Response {
	p.env.t.Helper()
	sep := "/"
	if strings.HasPrefix(call, ":") {
		sep = ""
	}
	return c.do("POST", itk+"projects/"+p.id+sep+call, body)
}

// nextSecond waits for the clock to enter a new second. validSince has one-second resolution
// and a token issued in the same second as a revocation stays valid, as in Firebase.
func nextSecond() {
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second + 10*time.Millisecond)))
}

func (p *adminProject) lookup(uid string) map[string]any {
	p.env.t.Helper()
	users := ok(p.env.t, p.admin(p.sa, "accounts:lookup", map[string]any{"localId": []string{uid}})).list("users")
	if len(users) != 1 {
		p.env.t.Fatalf("lookup %s: %v", uid, users)
	}
	return users[0]
}

// Only a service account of the project, or the owner's CLI token, reaches the Admin API.
// Everything a client or the emulator-mode SDKs could send is refused before any handler.
func TestAuthAdminCredentials(t *testing.T) {
	e := newEnv(t, nil)
	p := e.adminProject("chat-app")
	up := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "u@example.com", "password": "secret1"}))
	body := map[string]any{"localId": []string{up.str("localId")}}

	ok(t, p.admin(p.sa, "accounts:lookup", body))
	ok(t, p.sa.do("POST", itk+"projects/"+p.number+"/accounts:lookup", body)) // by number
	ok(t, p.admin(e.withBearer(p.owner.pat()), "accounts:lookup", body))

	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x","aud":"chat-app"}`)) + "."
	for name, c := range map[string]*Client{
		"no credential":  e.client(),
		"Bearer owner":   e.withBearer("owner"),
		"ID token":       e.withBearer(up.str("idToken")),
		"alg none":       e.withBearer(unsigned),
		"API key":        e.withBearer(p.key),
		"refresh token":  e.withBearer(up.str("refreshToken")),
		"made-up token":  e.withBearer("aruma_sat_" + strings.Repeat("A", 43)),
		"basic":          {env: e, cookies: map[string]*http.Cookie{}, header: map[string]string{"Authorization": "Basic b3duZXI6"}},
		"console cookie": p.owner,
	} {
		t.Run(name, func(t *testing.T) {
			r := p.admin(c, "accounts:lookup", body)
			expect(t, r, 401, "UNAUTHENTICATED")
			if strings.Contains(string(r.Body), "u@example.com") {
				t.Fatal("refused call leaked data")
			}
		})
	}
	// An API key in the query, as client calls carry it, is no credential either.
	expect(t, e.client().do("POST", itk+"projects/chat-app/accounts:lookup?key="+p.key, body), 401, "UNAUTHENTICATED")

	// Another project's service account, another owner's token, an unknown project: one
	// uniform refusal.
	p.owner.project("other-app")
	_, otherKey := p.owner.serviceAccountKey("other-app", "backend")
	otherToken, _, err := exchange(t, otherKey)
	if err != nil {
		t.Fatal(err)
	}
	stranger := e.account("stranger@example.com")
	for name, c := range map[string]*Client{
		"other project's service account": e.withBearer(otherToken),
		"another owner's token":           e.withBearer(stranger.pat()),
	} {
		t.Run(name, func(t *testing.T) { expect(t, p.admin(c, "accounts:lookup", body), 403, "PERMISSION_DENIED") })
	}
	expect(t, p.sa.do("POST", itk+"projects/no-such-project/accounts:lookup", body), 403, "PERMISSION_DENIED")

	// No CORS: browsers may not call the Admin API.
	r := e.client().do("OPTIONS", itk+"projects/chat-app/accounts:lookup", nil)
	if r.Header.Get("Access-Control-Allow-Origin") != "" || r.Status == 204 {
		t.Fatalf("admin preflight: %d %v", r.Status, r.Header)
	}
	if h := p.admin(p.sa, "accounts:lookup", body).Header.Get("Access-Control-Allow-Origin"); h != "" {
		t.Fatalf("admin CORS header: %q", h)
	}
	if !strings.Contains(e.Logs.String(), "rejected emulator owner token") {
		t.Fatal("Bearer owner must be logged")
	}
}

// Admin-only fields sent by a client are ignored: a client can neither set claims, disable or
// verify itself, nor reach another user.
func TestAuthAdminFieldsFromClient(t *testing.T) {
	e := newEnv(t, nil)
	p := e.adminProject("chat-app")
	me := ok(t, p.call(itk+"accounts:signUp", map[string]any{
		"email": "me@example.com", "password": "secret1", "localId": "chosen-uid", "emailVerified": true,
		"customAttributes": `{"admin":true}`, "disabled": true,
	}))
	other := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "other@example.com", "password": "secret1"}))
	if me.str("localId") == "chosen-uid" {
		t.Fatal("a client chose its uid")
	}
	ok(t, p.call(itk+"accounts:update", map[string]any{
		"idToken": me.str("idToken"), "localId": other.str("localId"), "customAttributes": `{"admin":true}`,
		"disableUser": true, "emailVerified": true, "validSince": "0", "displayName": "Me",
	}))
	for _, uid := range []string{me.str("localId"), other.str("localId")} {
		u := p.lookup(uid)
		if u["customAttributes"] != nil || u["disabled"] == true || u["emailVerified"] == true {
			t.Fatalf("client changed admin fields of %s: %v", uid, u)
		}
	}
	assertEqual(t, "other's name", p.lookup(other.str("localId"))["displayName"], nil)
	if c := claims(t, ok(t, p.refresh(me.str("refreshToken"))).str("id_token")); c["admin"] != nil {
		t.Fatalf("claims after client update: %v", c)
	}
	// Admin lookups and deletes by uid need an admin credential.
	authErr(t, p.call(itk+"accounts:lookup", map[string]any{"localId": []string{other.str("localId")}}), "INVALID_ID_TOKEN")
	authErr(t, p.call(itk+"accounts:delete", map[string]any{"localId": other.str("localId")}), "INVALID_ID_TOKEN")
	p.lookup(other.str("localId"))
}

// AC-A14: the management calls answer in Identity Toolkit shapes with the codes the SDKs map.
func TestAuthAdminUserManagement(t *testing.T) {
	e := newEnv(t, nil)
	p := e.adminProject("chat-app")

	created := ok(t, p.admin(p.sa, "accounts", map[string]any{
		"localId": "user-1", "email": "Sara@Example.com", "password": "secret1", "displayName": "سارة",
		"photoUrl": "https://example.com/s.png", "emailVerified": true,
	}))
	assertEqual(t, "localId", created.str("localId"), "user-1")
	u := p.lookup("user-1")
	for k, want := range map[string]any{
		"email": "Sara@Example.com", "displayName": "سارة", "photoUrl": "https://example.com/s.png",
		"emailVerified": true, "passwordHash": "UkVEQUNURUQ=",
	} {
		assertEqual(t, k, u[k], want)
	}
	for _, k := range []string{"createdAt", "validSince"} {
		if _, isString := u[k].(string); !isString {
			t.Fatalf("%s must be a quoted integer (the Go SDK requires it): %v", k, u)
		}
	}
	// The new user signs in with the password the admin set.
	ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "secret1"}))
	generated := ok(t, p.admin(p.sa, "accounts", map[string]any{})).str("localId")
	if len(generated) != 28 {
		t.Fatalf("generated uid %q", generated)
	}

	authErr(t, p.admin(p.sa, "accounts", map[string]any{"email": "sara@example.com"}), "EMAIL_EXISTS")
	authErr(t, p.admin(p.sa, "accounts", map[string]any{"localId": "user-1"}), "DUPLICATE_LOCAL_ID")
	authErr(t, p.admin(p.sa, "accounts", map[string]any{"email": "nope"}), "INVALID_EMAIL")
	authErr(t, p.admin(p.sa, "accounts", map[string]any{"email": "w@example.com", "password": "123"}), "WEAK_PASSWORD")
	authErr(t, p.admin(p.sa, "accounts", map[string]any{"localId": strings.Repeat("x", 129)}), "INVALID_LOCAL_ID")
	authErr(t, p.admin(p.sa, "accounts", map[string]any{"phoneNumber": "+15555550100"}), "OPERATION_NOT_ALLOWED")

	// getUsers: by uid and email together, unknown identifiers skipped; no match leaves users out.
	both := ok(t, p.admin(p.sa, "accounts:lookup", map[string]any{"localId": []string{"user-1", "ghost"}, "email": []string{"SARA@example.com", "ghost@example.com"}}))
	if users := both.list("users"); len(users) != 1 || users[0]["localId"] != "user-1" {
		t.Fatalf("lookup: %s", both.Body)
	}
	none := ok(t, p.admin(p.sa, "accounts:lookup", map[string]any{"localId": []string{"ghost"}}))
	if _, present := none.JSON["users"]; present {
		t.Fatalf("no match must leave users out: %s", none.Body)
	}

	// updateUser: profile, clearing with deleteAttribute, password.
	upd := ok(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": "user-1", "displayName": "Sara", "deleteAttribute": []string{"PHOTO_URL"}, "password": "secret2"}))
	assertEqual(t, "displayName", upd.str("displayName"), "Sara")
	assertEqual(t, "photoUrl", p.lookup("user-1")["photoUrl"], nil)
	authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "secret1"}), "INVALID_LOGIN_CREDENTIALS")
	ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "secret2"}))
	authErr(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": "ghost", "displayName": "x"}), "USER_NOT_FOUND")
	authErr(t, p.admin(p.sa, "accounts:update", map[string]any{"displayName": "x"}), "MISSING_LOCAL_ID")
	authErr(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": generated, "email": "SARA@example.com"}), "EMAIL_EXISTS")

	// deleteUser.
	ok(t, p.admin(p.sa, "accounts:delete", map[string]any{"localId": generated}))
	authErr(t, p.admin(p.sa, "accounts:delete", map[string]any{"localId": generated}), "USER_NOT_FOUND")

	// listUsers: stable uid order across pages, every user exactly once.
	var want []string
	for i := range 5 {
		want = append(want, ok(t, p.admin(p.sa, "accounts", map[string]any{"localId": fmt.Sprintf("list-%d", i)})).str("localId"))
	}
	want = append(want, "user-1")
	slices.Sort(want)
	var got []string
	token := ""
	for pages := 0; ; pages++ {
		r := ok(t, p.sa.do("GET", itk+"projects/chat-app/accounts:batchGet?maxResults=2&nextPageToken="+token, nil))
		for _, u := range r.list("users") {
			got = append(got, u["localId"].(string))
		}
		if token = r.str("nextPageToken"); token == "" {
			break
		}
		if pages > 10 {
			t.Fatal("listing does not end")
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	authErr(t, p.sa.do("GET", itk+"projects/chat-app/accounts:batchGet?maxResults=1001", nil), "INVALID_PAGE_SELECTION")

	// deleteUsers: unknown uids are not errors; without force, enabled users are refused.
	refused := ok(t, p.admin(p.sa, "accounts:batchDelete", map[string]any{"localIds": []string{"list-0", "list-1"}}))
	if errs := refused.list("errors"); len(errs) != 2 || !strings.HasPrefix(errs[1]["message"].(string), "NOT_DISABLED") || errs[1]["index"] != float64(1) {
		t.Fatalf("batchDelete without force: %s", refused.Body)
	}
	all := ok(t, p.admin(p.sa, "accounts:batchDelete", map[string]any{"localIds": []string{"list-0", "list-1", "ghost"}, "force": true}))
	if _, present := all.JSON["errors"]; present {
		t.Fatalf("batchDelete: %s", all.Body)
	}
	if n := len(ok(t, p.admin(p.sa, "accounts:lookup", map[string]any{"localId": []string{"list-0", "list-1"}})).list("users")); n != 0 {
		t.Fatalf("%d users survived batchDelete", n)
	}
}

// AC-A13: claims set by the admin appear in ID tokens after a refresh, not before; reserved
// names and oversized claims are refused.
func TestAuthAdminCustomClaims(t *testing.T) {
	e := newEnv(t, nil)
	p := e.adminProject("chat-app")
	up := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "c@example.com", "password": "secret1"}))
	uid := up.str("localId")

	ok(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": uid, "customAttributes": `{"role":"admin","level":3}`}))
	if claims(t, up.str("idToken"))["role"] != nil {
		t.Fatal("the existing ID token cannot change")
	}
	c := claims(t, ok(t, p.refresh(up.str("refreshToken"))).str("id_token"))
	assertEqual(t, "role", c["role"], "admin")
	assertEqual(t, "level", c["level"], float64(3))
	var stored map[string]any
	if err := json.Unmarshal([]byte(p.lookup(uid)["customAttributes"].(string)), &stored); err != nil || stored["role"] != "admin" || len(stored) != 2 {
		t.Fatalf("customAttributes: %v %v", stored, err)
	}

	authErr(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": uid, "customAttributes": `{"sub":"x"}`}), "FORBIDDEN_CLAIM")
	authErr(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": uid, "customAttributes": `{"firebase":{}}`}), "FORBIDDEN_CLAIM")
	authErr(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": uid, "customAttributes": `{"big":"` + strings.Repeat("x", 1000) + `"}`}), "CLAIMS_TOO_LARGE")
	authErr(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": uid, "customAttributes": `[1]`}), "INVALID_CLAIMS")
	assertEqual(t, "claims kept after refusals", claims(t, ok(t, p.refresh(up.str("refreshToken"))).str("id_token"))["role"], "admin")

	// {} clears them.
	ok(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": uid, "customAttributes": "{}"}))
	if c := claims(t, ok(t, p.refresh(up.str("refreshToken"))).str("id_token")); c["role"] != nil {
		t.Fatalf("cleared claims still in the token: %v", c)
	}
	assertEqual(t, "customAttributes", p.lookup(uid)["customAttributes"], nil)
}

// AC-A11 and AC-A12 at the HTTP layer: revoking moves validSince and the refresh answers
// TOKEN_EXPIRED; disabling refuses sign-in and refresh until the user is enabled again.
func TestAuthAdminRevokeAndDisable(t *testing.T) {
	e := newEnv(t, nil)
	p := e.adminProject("chat-app")
	up := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "r@example.com", "password": "secret1"}))
	uid := up.str("localId")
	before := p.lookup(uid)["validSince"].(string)

	// Node sends validSince as a number and Go as a string; either revokes at the server's clock.
	nextSecond()
	ok(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": uid, "validSince": 1}))
	if after := p.lookup(uid)["validSince"].(string); after < before {
		t.Fatalf("validSince moved back: %s -> %s", before, after)
	}
	authErr(t, p.refresh(up.str("refreshToken")), "TOKEN_EXPIRED")
	authErr(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": up.str("idToken")}), "TOKEN_EXPIRED")
	in := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "r@example.com", "password": "secret1"}))
	ok(t, p.refresh(in.str("refreshToken")))

	ok(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": uid, "disableUser": true}))
	assertEqual(t, "disabled", p.lookup(uid)["disabled"], true)
	// The right password on a disabled account says so; a wrong one still says nothing.
	authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "r@example.com", "password": "secret1"}), "USER_DISABLED")
	authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "r@example.com", "password": "wrong!!"}), "INVALID_LOGIN_CREDENTIALS")
	authErr(t, p.refresh(in.str("refreshToken")), "USER_DISABLED")
	ok(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": uid, "disableUser": false}))
	ok(t, p.refresh(in.str("refreshToken")))
	ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "r@example.com", "password": "secret1"}))
}

// AC-A20: a session cookie carries the ID token's claims under the session issuer and keys,
// lives as long as asked within 5 minutes to 14 days, and dies with a revocation.
func TestAuthAdminSessionCookie(t *testing.T) {
	e := newEnv(t, nil)
	p := e.adminProject("chat-app")
	up := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "s@example.com", "password": "secret1"}))
	ok(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": up.str("localId"), "customAttributes": `{"role":"editor"}`}))
	idToken := ok(t, p.refresh(up.str("refreshToken"))).str("id_token")

	cookie := ok(t, p.admin(p.sa, ":createSessionCookie", map[string]any{"idToken": idToken, "validDuration": 3600})).str("sessionCookie")
	c, id := claims(t, cookie), claims(t, idToken)
	assertEqual(t, "iss", c["iss"], e.URL+"/session-issuers/chat-app")
	for _, k := range []string{"aud", "sub", "auth_time", "role", "email"} {
		assertEqual(t, k, c[k], id[k])
	}
	if c["exp"].(float64)-c["iat"].(float64) != 3600 {
		t.Fatalf("cookie lifetime: %v", c)
	}
	// Go sends validDuration as an integer, and a string is read the same.
	ok(t, p.admin(p.sa, ":createSessionCookie", map[string]any{"idToken": idToken, "validDuration": "1209600"}))

	// Signed with the session keys: the session JWKS has its kid, the ID token JWKS does not.
	kid := func(jwt string) string {
		b, _ := base64.RawURLEncoding.DecodeString(strings.Split(jwt, ".")[0])
		var h struct{ Kid string }
		_ = json.Unmarshal(b, &h)
		return h.Kid
	}
	has := func(path, kid string) bool {
		for _, k := range ok(t, e.client().do("GET", path, nil)).list("keys") {
			if k["kid"] == kid {
				return true
			}
		}
		return false
	}
	if !has("/session-issuers/chat-app/.well-known/jwks.json", kid(cookie)) || has("/issuers/chat-app/.well-known/jwks.json", kid(cookie)) {
		t.Fatal("cookie must be signed with the session cookie keys only")
	}
	// A cookie is not an ID token.
	authErr(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": cookie}), "INVALID_ID_TOKEN")
	authErr(t, p.admin(p.sa, ":createSessionCookie", map[string]any{"idToken": cookie, "validDuration": 3600}), "INVALID_ID_TOKEN")

	for _, d := range []any{299, 1209601, 0, -5, "1e30", nil} {
		authErr(t, p.admin(p.sa, ":createSessionCookie", map[string]any{"idToken": idToken, "validDuration": d}), "INVALID_DURATION")
	}
	authErr(t, p.admin(p.sa, ":createSessionCookie", map[string]any{"idToken": "nope", "validDuration": 3600}), "INVALID_ID_TOKEN")
	authErr(t, p.admin(p.sa, ":createSessionCookie", map[string]any{"validDuration": 3600}), "INVALID_ID_TOKEN")

	// Server-side verification, and revocation.
	auth, proj := e.App.Auth, identity.Project{ID: "chat-app"}
	ctx := context.Background()
	var err error
	if proj, err = auth.ProjectByID(ctx, "chat-app"); err != nil {
		t.Fatal(err)
	}
	if u, _, err := auth.VerifySessionCookie(ctx, proj, cookie); err != nil || u.UID != up.str("localId") {
		t.Fatalf("verify: %v %v", u.UID, err)
	}
	if _, _, err := auth.VerifySessionCookie(ctx, proj, idToken); err != identity.ErrInvalidIDToken {
		t.Fatalf("an ID token passed as a cookie: %v", err)
	}
	nextSecond()
	ok(t, p.admin(p.sa, "accounts:update", map[string]any{"localId": up.str("localId"), "validSince": "1"}))
	if _, _, err := auth.VerifySessionCookie(ctx, proj, cookie); err != identity.ErrTokenExpired {
		t.Fatalf("revoked cookie: %v", err)
	}
	authErr(t, p.admin(p.sa, ":createSessionCookie", map[string]any{"idToken": idToken, "validDuration": 3600}), "TOKEN_EXPIRED")
}

// generatePasswordResetLink and friends return a working link and email nothing.
func TestAuthAdminEmailActionLinks(t *testing.T) {
	e := newEnv(t, nil)
	p := e.adminProject("chat-app")
	p.signUp("sara@example.com", "secret1")
	before := len(p.mailbox())
	r := ok(t, p.admin(p.sa, "accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "sara@example.com", "returnOobLink": true}))
	_, q := linkCode(t, r.str("oobLink"))
	if len(p.mailbox()) != before {
		t.Fatal("an admin link must not be emailed")
	}
	ok(t, p.call(itk+"accounts:resetPassword", map[string]any{"oobCode": q.Get("oobCode"), "newPassword": "new-secret-1"}))
	ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "new-secret-1"}))
	authErr(t, p.admin(p.sa, "accounts:sendOobCode", map[string]any{"requestType": "PASSWORD_RESET", "email": "nobody@example.com", "returnOobLink": true}), "EMAIL_NOT_FOUND")
	authErr(t, p.admin(p.sa, "accounts:sendOobCode", map[string]any{"requestType": "EMAIL_SIGNIN", "email": "sara@example.com", "returnOobLink": true}), "OPERATION_NOT_ALLOWED")
}
