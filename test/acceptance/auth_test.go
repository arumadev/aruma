package acceptance

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	itk = "/identitytoolkit.googleapis.com/v1/"
	sts = "/securetoken.googleapis.com/v1/token"
	rp  = "/www.googleapis.com/identitytoolkit/v3/relyingparty/"
)

// authProject is a project with a web app, seen from a client SDK.
type authProject struct {
	env    *Env
	owner  *Client
	id     string
	number string
	key    string
}

func (e *Env) authProject(id string) *authProject {
	e.t.Helper()
	owner := e.owner()
	p := owner.project(id)
	web := owner.app(id, "WEB", "")
	key := owner.mustOK(owner.do("GET", "/console/v1/projects/"+id+"/apiKeys/"+web.str("apiKeyId"), nil)).str("keyString")
	return &authProject{env: e, owner: owner, id: id, number: p.str("projectNumber"), key: key}
}

// enableAnonymous turns on anonymous sign-in, which new projects start without, as in Firebase.
func (p *authProject) enableAnonymous() {
	p.env.t.Helper()
	if _, err := p.env.Pool.Exec(context.Background(),
		`INSERT INTO auth.project_config (project_number, anonymous_enabled) VALUES ($1, true)
		 ON CONFLICT (project_number) DO UPDATE SET anonymous_enabled = true`, p.number); err != nil {
		p.env.t.Fatal(err)
	}
}

// call posts a client request with the project's API key, as the SDKs do.
func (p *authProject) call(path string, body any) *Response {
	p.env.t.Helper()
	return p.env.client().do("POST", path+"?key="+p.key, body)
}

// refresh exchanges a refresh token the way the web SDK does: a form in snake_case.
func (p *authProject) refresh(token string) *Response {
	p.env.t.Helper()
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}}
	resp, err := http.Post(p.env.URL+sts+"?key="+p.key, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		p.env.t.Fatal(err)
	}
	return decodeResponse(p.env.t, resp)
}

func decodeResponse(t *testing.T, resp *http.Response) *Response {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	out := &Response{Status: resp.StatusCode, Header: resp.Header, Body: b}
	_ = json.Unmarshal(b, &out.JSON)
	return out
}

// authErr asserts an Identity Toolkit error: HTTP 400 and the code as the message, in the
// envelope the SDKs parse.
func authErr(t *testing.T, r *Response, code string) {
	t.Helper()
	msg := r.str("error", "message")
	if r.Status != 400 || (msg != code && !strings.HasPrefix(msg, code+" : ")) {
		t.Fatalf("want 400 %s, got %d: %s", code, r.Status, r.Body)
	}
	errs, _ := r.JSON["error"].(map[string]any)["errors"].([]any)
	if len(errs) != 1 || errs[0].(map[string]any)["reason"] != "invalid" || errs[0].(map[string]any)["domain"] != "global" {
		t.Fatalf("error envelope: %s", r.Body)
	}
}

func ok(t *testing.T, r *Response) *Response {
	t.Helper()
	if r.Status != 200 {
		t.Fatalf("status %d: %s", r.Status, r.Body)
	}
	return r
}

// claims decodes a JWT payload without verifying it.
func claims(t *testing.T, jwt string) map[string]any {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", jwt)
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAuthEmailPasswordLifecycle(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")

	up := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "sara@example.com", "password": "secret1", "returnSecureToken": true}))
	if up.str("kind") != "identitytoolkit#SignupNewUserResponse" || up.str("expiresIn") != "3600" || len(up.str("localId")) != 28 {
		t.Fatalf("signUp response: %s", up.Body)
	}
	c := claims(t, up.str("idToken"))
	assertEqual(t, "iss", c["iss"], e.URL+"/issuers/chat-app")
	assertEqual(t, "aud", c["aud"], "chat-app")
	assertEqual(t, "sub", c["sub"], up.str("localId"))
	assertEqual(t, "sign_in_provider", c["firebase"].(map[string]any)["sign_in_provider"], "password")
	if c["exp"].(float64)-c["iat"].(float64) != 3600 {
		t.Fatalf("lifetime: %v", c)
	}

	authErr(t, p.call(itk+"accounts:signUp", map[string]any{"email": "SARA@example.com", "password": "secret1"}), "EMAIL_EXISTS")
	authErr(t, p.call(itk+"accounts:signUp", map[string]any{"email": "new@example.com", "password": "123"}), "WEAK_PASSWORD")
	authErr(t, p.call(itk+"accounts:signUp", map[string]any{"email": "not-an-email", "password": "secret1"}), "INVALID_EMAIL")
	authErr(t, p.call(itk+"accounts:signUp", map[string]any{"password": "secret1"}), "MISSING_EMAIL")

	in := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "secret1", "returnSecureToken": true}))
	if in.JSON["registered"] != true || in.str("localId") != up.str("localId") {
		t.Fatalf("signIn response: %s", in.Body)
	}
	// Enumeration protection (the default): unknown email and wrong password look the same.
	authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "wrong!!"}), "INVALID_LOGIN_CREDENTIALS")
	authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "nobody@example.com", "password": "secret1"}), "INVALID_LOGIN_CREDENTIALS")

	look := ok(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": in.str("idToken")}))
	users := look.list("users")
	if len(users) != 1 || users[0]["email"] != "sara@example.com" || users[0]["passwordHash"] == "" || users[0]["passwordHash"] == nil {
		t.Fatalf("lookup: %s", look.Body)
	}
	if strings.Contains(string(look.Body), "argon2") {
		t.Fatal("lookup leaks the password hash")
	}
	if _, isString := users[0]["createdAt"].(string); !isString {
		t.Fatalf("createdAt must be a string of ms: %s", look.Body)
	}

	// Refresh keeps the refresh token and mints a new ID token for the same user.
	rt := ok(t, p.refresh(in.str("refreshToken")))
	if rt.str("refresh_token") != in.str("refreshToken") || rt.str("access_token") != rt.str("id_token") ||
		rt.str("user_id") != up.str("localId") || rt.str("project_id") != p.number || rt.str("token_type") != "Bearer" {
		t.Fatalf("token response: %s", rt.Body)
	}
	// iOS sends the grant as JSON in camelCase.
	ok(t, p.call(sts, map[string]any{"grantType": "refresh_token", "refreshToken": in.str("refreshToken")}))
	authErr(t, p.call(sts, map[string]any{"grantType": "refresh_token", "refreshToken": "nope"}), "INVALID_REFRESH_TOKEN")
	authErr(t, p.call(sts, map[string]any{"refreshToken": "x"}), "MISSING_GRANT_TYPE")
	authErr(t, p.call(sts, map[string]any{"grantType": "password"}), "INVALID_GRANT_TYPE")

	// A password change revokes the other sessions and returns a fresh one.
	upd := ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": in.str("idToken"), "password": "secret2", "returnSecureToken": true}))
	if upd.str("idToken") == "" || upd.str("refreshToken") == "" {
		t.Fatalf("update must return a new session: %s", upd.Body)
	}
	authErr(t, p.refresh(in.str("refreshToken")), "TOKEN_EXPIRED")
	authErr(t, p.refresh(up.str("refreshToken")), "TOKEN_EXPIRED")
	ok(t, p.refresh(upd.str("refreshToken")))
	authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "secret1"}), "INVALID_LOGIN_CREDENTIALS")
	fresh := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "sara@example.com", "password": "secret2"}))

	// Profile: set, then clear with null as the web SDK does.
	prof := ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": fresh.str("idToken"), "displayName": "سارة", "photoUrl": "https://example.com/s.png"}))
	assertEqual(t, "displayName", prof.str("displayName"), "سارة")
	if prof.str("idToken") != "" {
		t.Fatal("a profile change must not start a new session")
	}
	cleared := ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": fresh.str("idToken"), "displayName": nil}))
	assertEqual(t, "cleared displayName", cleared.str("displayName"), "")
	assertEqual(t, "photoUrl kept", cleared.str("photoUrl"), "https://example.com/s.png")

	// Deleting the account signs every session out with USER_NOT_FOUND.
	ok(t, p.call(itk+"accounts:delete", map[string]any{"idToken": fresh.str("idToken")}))
	authErr(t, p.refresh(fresh.str("refreshToken")), "USER_NOT_FOUND")
	authErr(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": fresh.str("idToken")}), "USER_NOT_FOUND")

	var created, deleted int
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT count(*) FILTER (WHERE type = 'auth.user.created'), count(*) FILTER (WHERE type = 'auth.user.deleted')
		 FROM events.changes WHERE service = 'auth' AND subject = $1`, up.str("localId")).Scan(&created, &deleted); err != nil {
		t.Fatal(err)
	}
	if created != 1 || deleted != 1 {
		t.Fatalf("events: created %d, deleted %d", created, deleted)
	}
}

// AC-A2: an anonymous user who signs up with an email keeps their uid.
func TestAuthAnonymousUpgradeKeepsUID(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	authErr(t, p.call(itk+"accounts:signUp", map[string]any{}), "OPERATION_NOT_ALLOWED")
	p.enableAnonymous()

	anon := ok(t, p.call(itk+"accounts:signUp", map[string]any{"returnSecureToken": true}))
	c := claims(t, anon.str("idToken"))
	assertEqual(t, "provider", c["firebase"].(map[string]any)["sign_in_provider"], "anonymous")
	assertEqual(t, "provider_id", c["provider_id"], "anonymous")
	assertEqual(t, "email", anon.str("email"), "")

	linked := ok(t, p.call(itk+"accounts:signUp", map[string]any{"idToken": anon.str("idToken"), "email": "anon@example.com", "password": "secret1"}))
	assertEqual(t, "uid", linked.str("localId"), anon.str("localId"))
	assertEqual(t, "provider after link", claims(t, linked.str("idToken"))["firebase"].(map[string]any)["sign_in_provider"], "password")
	in := ok(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "anon@example.com", "password": "secret1"}))
	assertEqual(t, "uid after sign-in", in.str("localId"), anon.str("localId"))
}

func TestAuthDisabledUser(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "d@example.com", "password": "secret1"}))
	if _, err := e.Pool.Exec(context.Background(), `UPDATE auth.users SET disabled = true WHERE uid = $1`, up.str("localId")); err != nil {
		t.Fatal(err)
	}
	authErr(t, p.refresh(up.str("refreshToken")), "USER_DISABLED")
	authErr(t, p.call(itk+"accounts:lookup", map[string]any{"idToken": up.str("idToken")}), "USER_DISABLED")
	// Under enumeration protection a wrong password says nothing about the account's state.
	authErr(t, p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "d@example.com", "password": "wrong!!"}), "INVALID_LOGIN_CREDENTIALS")
}

func TestAuthKeysAndProjects(t *testing.T) {
	e := newEnv(t, nil)
	a := e.authProject("alpha-app")
	up := ok(t, a.call(itk+"accounts:signUp", map[string]any{"email": "x@example.com", "password": "secret1"}))

	b := &authProject{env: e, owner: a.owner, id: "beta-app"}
	a.owner.project("beta-app")
	web := a.owner.app("beta-app", "WEB", "")
	b.key = a.owner.mustOK(a.owner.do("GET", "/console/v1/projects/beta-app/apiKeys/"+web.str("apiKeyId"), nil)).str("keyString")

	// Tokens of one project mean nothing in another.
	authErr(t, b.call(itk+"accounts:lookup", map[string]any{"idToken": up.str("idToken")}), "INVALID_ID_TOKEN")
	authErr(t, b.refresh(up.str("refreshToken")), "INVALID_REFRESH_TOKEN")
	authErr(t, b.call(itk+"accounts:signInWithPassword", map[string]any{"email": "x@example.com", "password": "secret1"}), "INVALID_LOGIN_CREDENTIALS")

	bad := e.client().do("POST", itk+"accounts:signUp?key=AruNotARealKeyAtAllxxxxxxxxxxxxxxxxxxxx", map[string]any{})
	if bad.Status != 400 || !strings.HasPrefix(bad.str("error", "message"), "API key not valid") {
		t.Fatalf("bad key: %d %s", bad.Status, bad.Body)
	}
	if r := e.client().do("POST", itk+"accounts:signUp", map[string]any{}); r.Status != 403 {
		t.Fatalf("missing key: %d %s", r.Status, r.Body)
	}
	if r := e.client().do("POST", itk+"accounts:resetEverything?key="+a.key, map[string]any{}); r.Status != 404 {
		t.Fatalf("unknown call: %d", r.Status)
	}
	// Emulator-only management routes are never served (ADR-014).
	if r := e.client().do("DELETE", "/emulator/v1/projects/alpha-app/accounts", nil); r.Status == 200 {
		t.Fatal("emulator reset route must not exist")
	}
}

func TestAuthV3Aliases(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := ok(t, p.call(rp+"signupNewUser", map[string]any{"email": "ios@example.com", "password": "secret1", "returnSecureToken": true}))
	in := ok(t, p.call(rp+"verifyPassword", map[string]any{"email": "ios@example.com", "password": "secret1", "returnSecureToken": true}))
	assertEqual(t, "uid", in.str("localId"), up.str("localId"))
	look := ok(t, p.call(rp+"getAccountInfo", map[string]any{"idToken": in.str("idToken")}))
	assertEqual(t, "kind", look.str("kind"), "identitytoolkit#GetAccountInfoResponse")
	cfg := e.client().do("GET", rp+"getProjectConfig?key="+p.key, nil)
	ok(t, cfg)
	assertEqual(t, "projectId", cfg.str("projectId"), p.number)
}

func TestAuthCORS(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")

	req, _ := http.NewRequest("OPTIONS", e.URL+itk+"accounts:signUp?key="+p.key, nil)
	req.Header.Set("Origin", "https://myapp.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type,x-client-version,x-firebase-gmpid")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 || resp.Header.Get("Access-Control-Allow-Origin") != "*" || resp.Header.Get("Access-Control-Allow-Credentials") != "" ||
		!strings.Contains(resp.Header.Get("Access-Control-Allow-Headers"), "x-firebase-gmpid") {
		t.Fatalf("preflight: %d %v", resp.StatusCode, resp.Header)
	}
	r := p.call(itk+"accounts:signUp", map[string]any{})
	if r.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("client responses must allow any origin")
	}
	// The console and Management API stay closed to other origins (ADR-010 S-8).
	if h := e.client().do("GET", "/console/v1/me", nil).Header.Get("Access-Control-Allow-Origin"); h != "" {
		t.Fatalf("console CORS: %q", h)
	}
}

// The ID token verifies against the issuer's published keys, as any OIDC library would.
func TestAuthDiscoveryAndJWKS(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	p.enableAnonymous()
	up := ok(t, p.call(itk+"accounts:signUp", map[string]any{"returnSecureToken": true}))

	disc := ok(t, e.client().do("GET", "/issuers/chat-app/.well-known/openid-configuration", nil))
	assertEqual(t, "issuer", disc.str("issuer"), e.URL+"/issuers/chat-app")
	jwksURL := disc.str("jwks_uri")
	jwks := ok(t, e.client().do("GET", strings.TrimPrefix(jwksURL, e.URL), nil))
	keys := jwks.list("keys")
	if len(keys) == 0 {
		t.Fatalf("no keys: %s", jwks.Body)
	}
	tok := up.str("idToken")
	parts := strings.Split(tok, ".")
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var h struct{ Alg, Kid string }
	_ = json.Unmarshal(hb, &h)
	assertEqual(t, "alg", h.Alg, "RS256")
	var verified bool
	for _, k := range keys {
		if k["kid"] != h.Kid {
			continue
		}
		n, _ := base64.RawURLEncoding.DecodeString(k["n"].(string))
		eb, _ := base64.RawURLEncoding.DecodeString(k["e"].(string))
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(eb).Int64())}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		verified = rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig) == nil
	}
	if !verified {
		t.Fatal("ID token does not verify against the published JWKS")
	}
	if r := e.client().do("GET", "/issuers/no-such-project/.well-known/jwks.json", nil); r.Status != 404 {
		t.Fatalf("unknown issuer: %d", r.Status)
	}
	sess := ok(t, e.client().do("GET", "/session-issuers/chat-app/.well-known/openid-configuration", nil))
	assertEqual(t, "session issuer", sess.str("issuer"), e.URL+"/session-issuers/chat-app")
}

// customToken signs a token as the official createCustomToken does.
func customToken(t *testing.T, keyFile []byte, uid string, extra map[string]any, mutate func(map[string]any)) string {
	t.Helper()
	var sa struct {
		ClientEmail  string `json:"client_email"`
		PrivateKey   string `json:"private_key"`
		PrivateKeyID string `json:"private_key_id"`
	}
	if err := json.Unmarshal(keyFile, &sa); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	payload := map[string]any{
		"iss": sa.ClientEmail, "sub": sa.ClientEmail, "uid": uid, "iat": now, "exp": now + 3600,
		"aud": "https://identitytoolkit.googleapis.com/google.identity.identitytoolkit.v1.IdentityToolkit",
	}
	if extra != nil {
		payload["claims"] = extra
	}
	if mutate != nil {
		mutate(payload)
	}
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(map[string]string{"alg": "RS256", "typ": "JWT", "kid": sa.PrivateKeyID}) + "." + enc(payload)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(nil, k.(*rsa.PrivateKey), crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestAuthCustomToken(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	_, keyFile := p.owner.serviceAccountKey("chat-app", "backend")

	first := ok(t, p.call(itk+"accounts:signInWithCustomToken", map[string]any{"token": customToken(t, keyFile, "user-42", map[string]any{"role": "admin"}, nil), "returnSecureToken": true}))
	if first.JSON["isNewUser"] != true {
		t.Fatalf("first sign-in: %s", first.Body)
	}
	c := claims(t, first.str("idToken"))
	assertEqual(t, "sub", c["sub"], "user-42")
	assertEqual(t, "claim", c["role"], "admin")
	assertEqual(t, "provider", c["firebase"].(map[string]any)["sign_in_provider"], "custom")
	// Claims from the token survive a refresh.
	assertEqual(t, "claim after refresh", claims(t, ok(t, p.refresh(first.str("refreshToken"))).str("id_token"))["role"], "admin")

	again := ok(t, p.call(itk+"accounts:signInWithCustomToken", map[string]any{"token": customToken(t, keyFile, "user-42", nil, nil)}))
	if again.JSON["isNewUser"] != false {
		t.Fatalf("second sign-in: %s", again.Body)
	}

	for name, mutate := range map[string]func(map[string]any){
		"wrong audience": func(m map[string]any) { m["aud"] = "chat-app" },
		"expired": func(m map[string]any) {
			m["iat"] = time.Now().Add(-2 * time.Hour).Unix()
			m["exp"] = time.Now().Add(-time.Hour).Unix()
		},
		"too long":     func(m map[string]any) { m["exp"] = m["iat"].(int64) + 7200 },
		"no uid":       func(m map[string]any) { delete(m, "uid") },
		"iss mismatch": func(m map[string]any) { m["iss"] = "someone@else.example" },
	} {
		t.Run(name, func(t *testing.T) {
			authErr(t, p.call(itk+"accounts:signInWithCustomToken", map[string]any{"token": customToken(t, keyFile, "user-42", nil, mutate)}), "INVALID_CUSTOM_TOKEN")
		})
	}
	authErr(t, p.call(itk+"accounts:signInWithCustomToken", map[string]any{"token": customToken(t, keyFile, "u", map[string]any{"sub": "x"}, nil)}), "FORBIDDEN_CLAIM")
	tampered := customToken(t, keyFile, "user-42", nil, nil)
	tampered = tampered[:len(tampered)-4] + "AAAA"
	authErr(t, p.call(itk+"accounts:signInWithCustomToken", map[string]any{"token": tampered}), "INVALID_CUSTOM_TOKEN")
	authErr(t, p.call(itk+"accounts:signInWithCustomToken", map[string]any{}), "MISSING_CUSTOM_TOKEN")

	// A service account of another project: CREDENTIAL_MISMATCH.
	p.owner.project("other-app")
	_, otherKey := p.owner.serviceAccountKey("other-app", "backend")
	authErr(t, p.call(itk+"accounts:signInWithCustomToken", map[string]any{"token": customToken(t, otherKey, "user-42", nil, nil)}), "CREDENTIAL_MISMATCH")
}

func TestAuthRecentLoginRequired(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	up := ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "old@example.com", "password": "secret1"}))
	// Age the sign-in: the refresh token keeps the original auth_time.
	if _, err := e.Pool.Exec(context.Background(), `UPDATE auth.refresh_tokens SET auth_time = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	old := ok(t, p.refresh(up.str("refreshToken"))).str("id_token")
	authErr(t, p.call(itk+"accounts:update", map[string]any{"idToken": old, "password": "secret2"}), "CREDENTIAL_TOO_OLD_LOGIN_AGAIN")
	authErr(t, p.call(itk+"accounts:delete", map[string]any{"idToken": old}), "CREDENTIAL_TOO_OLD_LOGIN_AGAIN")
	ok(t, p.call(itk+"accounts:update", map[string]any{"idToken": old, "displayName": "fine"}))
}

func TestAuthPasswordBackoff(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	ok(t, p.call(itk+"accounts:signUp", map[string]any{"email": "v@example.com", "password": "secret1"}))
	var last *Response
	for range 12 {
		last = p.call(itk+"accounts:signInWithPassword", map[string]any{"email": "v@example.com", "password": "guess!!"})
	}
	authErr(t, last, "TOO_MANY_ATTEMPTS_TRY_LATER")
}

// AC-A17: phone, MFA and email-link calls fail as a disabled provider does.
func TestAuthDeferredMethods(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	for _, path := range []string{
		itk + "accounts:sendVerificationCode", itk + "accounts:signInWithPhoneNumber", itk + "accounts:signInWithEmailLink",
		"/identitytoolkit.googleapis.com/v2/accounts/mfaSignIn:start", rp + "verifyPhoneNumber",
	} {
		authErr(t, p.call(path, map[string]any{}), "OPERATION_NOT_ALLOWED")
	}
}
