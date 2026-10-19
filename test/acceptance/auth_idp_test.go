package acceptance

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/osama300/aurma-project/internal/app"
	identity "github.com/osama300/aurma-project/internal/services/auth/core"
)

// Sign-in with Google and Apple (AC-A8, AC-A9 as far as they run without the real providers)
// and the IdP tamper cases. Google and Apple are played by fakeIdP: an OpenID provider with its
// own RSA key and JWKS, which the server reaches only through a test-only app option. The
// handler page's script is not run here; the tests do what it does, step by step, from the
// data block the page carries.

const (
	googleClient  = "1234-web.apps.googleusercontent.com"
	googleSecret  = "google-client-secret-value"
	appleServices = "com.example.chat.web"
	appleTeam     = "TEAM123456"
	appleKeyID    = "KEY1234567"
)

// fakeIdP is a stand-in OpenID provider.
type fakeIdP struct {
	t   *testing.T
	srv *httptest.Server
	key *rsa.PrivateKey
	iss string

	// Google checks its client secret; Apple a client secret JWT signed with appleKey.
	clientSecret string
	appleKey     *ecdsa.PublicKey

	// consentAs, when set, makes GET /authorize consent at once as the user it returns, for
	// tests that drive a real browser.
	consentAs func(url.Values) map[string]any

	mu      sync.Mutex
	codes   map[string]fakeCode
	revoked []url.Values
}

type fakeCode struct {
	clientID, redirectURI, nonce, challenge string
	claims                                  map[string]any
}

func newFakeIdP(t *testing.T, iss string) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{t: t, key: key, iss: iss, codes: map[string]fakeCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB",
		}}})
	})
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("GET /authorize", f.consent)
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if !f.clientOK(r.Form) {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.revoked = append(f.revoked, r.Form)
		f.mu.Unlock()
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) endpoints(issuers ...string) identity.IdPEndpoints {
	return identity.IdPEndpoints{
		AuthURL: f.srv.URL + "/authorize", TokenURL: f.srv.URL + "/token", JWKSURL: f.srv.URL + "/jwks",
		RevokeURL: f.srv.URL + "/revoke", Issuers: issuers,
	}
}

func signRS256(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"})
	p, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// idToken is a valid ID token of this provider for aud, with extra claims on top.
func (f *fakeIdP) idToken(aud string, extra map[string]any) string {
	now := time.Now().Unix()
	c := map[string]any{"iss": f.iss, "aud": aud, "sub": "sub-default", "iat": now, "exp": now + 3600}
	for k, v := range extra {
		c[k] = v
	}
	return signRS256(f.t, f.key, c)
}

// authorize plays the user consenting at the provider: it checks the authorization request
// and returns the code and state the provider sends back.
func (f *fakeIdP) authorize(authURI string, claims map[string]any) (code, state string) {
	f.t.Helper()
	u, err := url.Parse(authURI)
	if err != nil || !strings.HasPrefix(authURI, f.srv.URL+"/authorize?") {
		f.t.Fatalf("authorization URL %q", authURI)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("state") == "" || q.Get("nonce") == "" || q.Get("client_id") == "" {
		f.t.Fatalf("authorization request: %v", q)
	}
	code = "code-" + hex.EncodeToString(randomBytes(8))
	f.mu.Lock()
	f.codes[code] = fakeCode{clientID: q.Get("client_id"), redirectURI: q.Get("redirect_uri"), nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), claims: claims}
	f.mu.Unlock()
	return code, q.Get("state")
}

// consent is the provider's sign-in page for a browser: it consents as consentAs says and
// returns to the app, by redirect or, for response_mode=form_post, by an auto-submitted form.
func (f *fakeIdP) consent(w http.ResponseWriter, r *http.Request) {
	if f.consentAs == nil {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	code, state := f.authorize(f.srv.URL+r.URL.String(), f.consentAs(q))
	if q.Get("response_mode") == "form_post" {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<form method="post" action="%s"><input type="hidden" name="code" value="%s"><input type="hidden" name="state" value="%s"><input type="hidden" name="user" value='{"name":{"firstName":"Layla","lastName":"Hassan"}}'></form><script>document.forms[0].submit()</script>`,
			html.EscapeString(q.Get("redirect_uri")), code, html.EscapeString(state))
		return
	}
	http.Redirect(w, r, q.Get("redirect_uri")+"?"+url.Values{"state": {state}, "code": {code}}.Encode(), http.StatusFound)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func (f *fakeIdP) clientOK(form url.Values) bool {
	if f.appleKey == nil {
		return form.Get("client_secret") == f.clientSecret
	}
	parts := strings.Split(form.Get("client_secret"), ".")
	if len(parts) != 3 {
		return false
	}
	var h, c map[string]any
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if json.Unmarshal(hb, &h) != nil || json.Unmarshal(cb, &c) != nil || len(sig) != 64 {
		return false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	return ecdsa.Verify(f.appleKey, sum[:], r, s) && h["alg"] == "ES256" && h["kid"] == appleKeyID &&
		c["iss"] == appleTeam && c["sub"] == appleServices && c["aud"] == "https://appleid.apple.com" &&
		form.Get("client_id") == appleServices
}

func (f *fakeIdP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	c, ok := f.codes[r.Form.Get("code")]
	delete(f.codes, r.Form.Get("code"))
	f.mu.Unlock()
	switch {
	case !ok || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("client_id") != c.clientID || r.Form.Get("redirect_uri") != c.redirectURI:
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
		return
	case !f.clientOK(r.Form):
		http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
		return
	case c.challenge != "":
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != c.challenge {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
	}
	claims := map[string]any{"nonce": c.nonce}
	for k, v := range c.claims {
		claims[k] = v
	}
	aud := c.clientID
	if a, ok := claims["aud"].(string); ok {
		aud = a
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": "access-" + r.Form.Get("code"), "id_token": f.idToken(aud, claims),
		"refresh_token": "refresh-" + r.Form.Get("code"), "expires_in": 3600, "token_type": "Bearer", "scope": "openid email profile",
	})
}

// idpEnv is a server whose project has Google and Apple enabled against fake providers.
type idpEnv struct {
	*authProject
	google, apple *fakeIdP
}

func newIdPEnv(t *testing.T) *idpEnv {
	t.Helper()
	google := newFakeIdP(t, "https://accounts.google.com")
	google.clientSecret = googleSecret
	apple := newFakeIdP(t, "https://appleid.apple.com")
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	apple.appleKey = &ecKey.PublicKey
	der, _ := x509.MarshalPKCS8PrivateKey(ecKey)
	applePEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	e := newEnv(t, nil, app.WithIdPEndpoints(
		google.endpoints("https://accounts.google.com", "accounts.google.com"),
		apple.endpoints("https://appleid.apple.com"), http.DefaultClient))
	p := e.authProject("chat-app")
	providers := "/console/v1/projects/chat-app/auth/providers/"
	p.owner.mustOK(p.owner.do("PATCH", providers+"google.com", map[string]any{"clientId": googleClient, "clientSecret": googleSecret, "enabled": true}))
	p.owner.mustOK(p.owner.do("PATCH", providers+"apple.com", map[string]any{
		"servicesId": appleServices, "teamId": appleTeam, "keyId": appleKeyID, "privateKey": applePEM, "enabled": true,
	}))
	return &idpEnv{authProject: p, google: google, apple: apple}
}

var pageDataRe = regexp.MustCompile(`<script type="application/json" id="aruma-idp">(.*?)</script>`)

type page struct {
	status int
	header http.Header
	html   string
	data   map[string]string
}

func readPage(t *testing.T, resp *http.Response, err error) page {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	pg := page{status: resp.StatusCode, header: resp.Header, html: string(b), data: map[string]string{}}
	if m := pageDataRe.FindStringSubmatch(pg.html); m != nil {
		if err := json.Unmarshal([]byte(m[1]), &pg.data); err != nil {
			t.Fatalf("page data %s: %v", m[1], err)
		}
	}
	return pg
}

// start opens the handler page as the SDK does.
func (x *idpEnv) start(path, provider, authType, redirectURL string) page {
	x.env.t.Helper()
	q := url.Values{
		"apiKey": {x.key}, "appName": {"[DEFAULT]"}, "authType": {authType}, "redirectUrl": {redirectURL},
		"v": {"13.0.0"}, "providerId": {provider}, "scopes": {"profile"}, "customParameters": {`{"prompt":"select_account","client_id":"evil"}`},
	}
	if strings.HasSuffix(authType, "Popup") {
		q.Set("eventId", "4760880973")
	}
	resp, err := http.Get(x.env.URL + path + "?" + q.Encode())
	return readPage(x.env.t, resp, err)
}

// authEvent is what the handler page script hands the SDK, reduced to what signInWithIdp
// receives.
type authEvent struct {
	requestURI, postBody, sessionID string
	finish                          page
}

// flow runs a popup or redirect sign-in through the handler pages up to the AuthEvent.
func (x *idpEnv) flow(provider, authType string, claims map[string]any) authEvent {
	x.env.t.Helper()
	t := x.env.t
	start := x.start("/emulator/auth/handler", provider, authType, "http://localhost:5173/app")
	if start.status != 200 || start.data["mode"] != "start" || start.data["sessionId"] == "" {
		t.Fatalf("start page %d: %v", start.status, start.data)
	}
	idp := x.google
	if provider == identity.AppleProvider {
		idp = x.apple
	}
	code, state := idp.authorize(start.data["authUri"], claims)
	if state != start.data["state"] {
		t.Fatal("state on the page differs from the one sent to the provider")
	}
	callback := x.env.URL + "/__/auth/handler"
	var finish page
	var ev authEvent
	if provider == identity.AppleProvider {
		form := url.Values{"code": {code}, "state": {state}, "user": {`{"name":{"firstName":"Layla","lastName":"Hassan"},"email":"ignored@example.com"}`}}
		resp, err := http.PostForm(callback, form)
		finish = readPage(t, resp, err)
		ev = authEvent{requestURI: finish.data["callbackUrl"], postBody: finish.data["postBody"]}
	} else {
		ev.requestURI = callback + "?" + url.Values{"state": {state}, "code": {code}}.Encode()
		resp, err := http.Get(ev.requestURI)
		finish = readPage(t, resp, err)
	}
	if finish.status != 200 || finish.data["mode"] != "finish" || finish.data["state"] != state || finish.data["authType"] != authType {
		t.Fatalf("finish page %d: %v", finish.status, finish.data)
	}
	ev.sessionID, ev.finish = start.data["sessionId"], finish
	return ev
}

func (x *idpEnv) signIn(ev authEvent, idToken string) *Response {
	x.env.t.Helper()
	body := map[string]any{"requestUri": ev.requestURI, "sessionId": ev.sessionID, "returnSecureToken": true, "returnIdpCredential": true}
	if ev.postBody != "" {
		body["postBody"] = ev.postBody
	}
	if idToken != "" {
		body["idToken"] = idToken
	}
	return x.call(itk+"accounts:signInWithIdp", body)
}

// direct signs in with a provider ID token, as signInWithCredential does.
func (x *idpEnv) direct(provider, token, rawNonce, idToken string) *Response {
	x.env.t.Helper()
	post := url.Values{"id_token": {token}, "providerId": {provider}}
	if rawNonce != "" {
		post.Set("nonce", rawNonce)
	}
	body := map[string]any{"requestUri": "http://localhost", "postBody": post.Encode(), "returnSecureToken": true, "returnIdpCredential": true}
	if idToken != "" {
		body["idToken"] = idToken
	}
	return x.call(itk+"accounts:signInWithIdp", body)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func googleClaims(sub, email string, verified bool) map[string]any {
	return map[string]any{"sub": sub, "email": email, "email_verified": verified, "name": "Sara Ali", "given_name": "Sara", "family_name": "Ali", "picture": "https://example.com/sara.png"}
}

func TestAuthIdPGooglePopup(t *testing.T) {
	x := newIdPEnv(t)
	start := x.start("/emulator/auth/handler", "google.com", "signInViaPopup", "http://localhost:5173/app")
	if start.header.Get("Cross-Origin-Opener-Policy") != "unsafe-none" || start.header.Get("X-Frame-Options") != "DENY" || start.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("handler headers: %v", start.header)
	}
	assertScriptHashes(t, start)
	u, _ := url.Parse(start.data["authUri"])
	q := u.Query()
	assertEqual(t, "redirect_uri", q.Get("redirect_uri"), x.env.URL+"/__/auth/handler")
	assertEqual(t, "client_id", q.Get("client_id"), googleClient)
	assertEqual(t, "prompt passes through", q.Get("prompt"), "select_account")
	assertEqual(t, "PKCE", q.Get("code_challenge_method"), "S256")
	assertEqual(t, "scope", q.Get("scope"), "openid email profile")

	ev := x.flow("google.com", "signInViaPopup", googleClaims("g-1", "sara@gmail.com", true))
	assertEqual(t, "eventId", ev.finish.data["eventId"], "4760880973")
	assertEqual(t, "apiKey", ev.finish.data["apiKey"], x.key)
	res := ok(t, x.signIn(ev, ""))
	for k, want := range map[string]any{
		"kind": "identitytoolkit#VerifyAssertionResponse", "providerId": "google.com", "federatedId": "https://accounts.google.com/g-1",
		"email": "sara@gmail.com", "emailVerified": true, "isNewUser": true, "displayName": "Sara Ali", "fullName": "Sara Ali",
		"firstName": "Sara", "lastName": "Ali", "photoUrl": "https://example.com/sara.png", "expiresIn": "3600",
	} {
		assertEqual(t, k, res.JSON[k], want)
	}
	if _, found := res.JSON["needConfirmation"]; found || res.str("oauthIdToken") == "" || res.str("oauthAccessToken") == "" || !strings.Contains(res.str("rawUserInfo"), `"id":"g-1"`) {
		t.Fatalf("signInWithIdp: %s", res.Body)
	}
	c := claims(t, res.str("idToken"))
	assertEqual(t, "sign_in_provider", c["firebase"].(map[string]any)["sign_in_provider"], "google.com")
	assertEqual(t, "email_verified", c["email_verified"], true)
	look := ok(t, x.call(itk+"accounts:lookup", map[string]any{"idToken": res.str("idToken")}))
	user := look.list("users")[0]
	assertEqual(t, "localId", user["localId"], res.str("localId"))
	info := user["providerUserInfo"].([]any)[0].(map[string]any)
	assertEqual(t, "provider", info["providerId"], "google.com")
	assertEqual(t, "rawId", info["rawId"], "g-1")

	// The second sign-in finds the same account (v3 name of the call).
	ev = x.flow("google.com", "signInViaPopup", googleClaims("g-1", "sara@gmail.com", true))
	again := x.call(rp+"verifyAssertion", map[string]any{"requestUri": ev.requestURI, "sessionId": ev.sessionID, "returnSecureToken": true})
	ok(t, again)
	assertEqual(t, "same uid", again.str("localId"), res.str("localId"))
	assertEqual(t, "isNewUser", again.JSON["isNewUser"], false)

	// Tamper: the same answer replayed.
	authErr(t, x.signIn(ev, ""), "INVALID_IDP_RESPONSE")
}

func TestAuthIdPGoogleRedirect(t *testing.T) {
	x := newIdPEnv(t)
	start := x.start("/__/auth/handler", "google.com", "signInViaRedirect", "http://localhost:5173/login")
	assertEqual(t, "mode", start.data["mode"], "start")
	ev := x.flow("google.com", "signInViaRedirect", googleClaims("g-2", "omar@gmail.com", true))
	assertEqual(t, "redirectUrl", ev.finish.data["redirectUrl"], "http://localhost:5173/app")
	assertEqual(t, "no eventId", ev.finish.data["eventId"], "")
	res := ok(t, x.signIn(ev, ""))
	assertEqual(t, "email", res.str("email"), "omar@gmail.com")

	// The provider sends the user back without consent.
	start = x.start("/__/auth/handler", "google.com", "signInViaRedirect", "http://localhost:5173/login")
	_, state := x.google.authorize(start.data["authUri"], nil)
	resp, err := http.Get(x.env.URL + "/__/auth/handler?" + url.Values{"state": {state}, "error": {"access_denied"}}.Encode())
	denied := readPage(t, resp, err)
	assertEqual(t, "mode", denied.data["mode"], "error")
	assertEqual(t, "errorCode", denied.data["errorCode"], "auth/user-cancelled")
	assertEqual(t, "redirectUrl", denied.data["redirectUrl"], "http://localhost:5173/login")
}

func TestAuthIdPAppleFormPostAndRevoke(t *testing.T) {
	x := newIdPEnv(t)
	start := x.start("/emulator/auth/handler", "apple.com", "signInViaPopup", "http://localhost:5173/app")
	u, _ := url.Parse(start.data["authUri"])
	assertEqual(t, "response_mode", u.Query().Get("response_mode"), "form_post")
	assertEqual(t, "scope", u.Query().Get("scope"), "name email")
	assertEqual(t, "client_id", u.Query().Get("client_id"), appleServices)

	ev := x.flow("apple.com", "signInViaPopup", map[string]any{"sub": "apple-1", "email": "relay@privaterelay.appleid.com", "email_verified": "true", "is_private_email": "true"})
	if !strings.Contains(ev.postBody, "code=") || ev.requestURI != x.env.URL+"/__/auth/handler" {
		t.Fatalf("apple event: %+v", ev)
	}
	res := ok(t, x.signIn(ev, ""))
	assertEqual(t, "displayName from the first consent", res.str("displayName"), "Layla Hassan")
	assertEqual(t, "email from the token, not the post", res.str("email"), "relay@privaterelay.appleid.com")
	assertEqual(t, "emailVerified", res.JSON["emailVerified"], true)
	assertEqual(t, "federatedId", res.str("federatedId"), "apple-1")
	uid := res.str("localId")

	// The Apple refresh token is kept, sealed, for revocation.
	var enc []byte
	if err := x.env.Pool.QueryRow(context.Background(), `SELECT refresh_token_enc FROM auth.idp_tokens WHERE uid = $1`, uid).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(enc), "refresh-code-") {
		t.Fatal("Apple refresh token stored in the clear")
	}

	// The app revokes an access token (revokeAccessToken).
	ok(t, x.call("/identitytoolkit.googleapis.com/v2/accounts:revokeToken", map[string]any{
		"providerId": "apple.com", "tokenType": "ACCESS_TOKEN", "token": res.str("oauthAccessToken"), "idToken": res.str("idToken"),
	}))
	authErr(t, x.call("/identitytoolkit.googleapis.com/v2/accounts:revokeToken", map[string]any{
		"providerId": "google.com", "tokenType": "ACCESS_TOKEN", "token": "x", "idToken": res.str("idToken"),
	}), "INVALID_PROVIDER_ID")

	// Deleting the account revokes the kept refresh token at Apple (AC-A9).
	ok(t, x.call(itk+"accounts:delete", map[string]any{"idToken": res.str("idToken")}))
	x.apple.mu.Lock()
	defer x.apple.mu.Unlock()
	if len(x.apple.revoked) != 2 || x.apple.revoked[0].Get("token_type_hint") != "access_token" ||
		x.apple.revoked[1].Get("token_type_hint") != "refresh_token" || !strings.HasPrefix(x.apple.revoked[1].Get("token"), "refresh-code-") {
		t.Fatalf("revocations at Apple: %v", x.apple.revoked)
	}
}

func TestAuthIdPDirectIDToken(t *testing.T) {
	x := newIdPEnv(t)
	tok := x.google.idToken(googleClient, googleClaims("g-3", "lina@gmail.com", true))
	res := ok(t, x.direct("google.com", tok, "", ""))
	assertEqual(t, "isNewUser", res.JSON["isNewUser"], true)
	assertEqual(t, "oauthIdToken", res.str("oauthIdToken"), tok)

	// Apple with a nonce: the token carries sha256(rawNonce).
	appleTok := x.apple.idToken(appleServices, map[string]any{"sub": "apple-2", "email": "a2@example.com", "email_verified": true, "nonce": sha256Hex("raw-nonce-1")})
	ok(t, x.direct("apple.com", appleTok, "raw-nonce-1", ""))
	authErr(t, x.direct("apple.com", appleTok, "another-nonce", ""), "MISSING_OR_INVALID_NONCE")
	authErr(t, x.direct("apple.com", appleTok, "", ""), "MISSING_OR_INVALID_NONCE")

	// Tamper: tokens for another client, from another issuer, under another key, expired.
	authErr(t, x.direct("google.com", x.google.idToken("other-client.apps.googleusercontent.com", googleClaims("g-4", "x@gmail.com", true)), "", ""), "INVALID_IDP_RESPONSE")
	authErr(t, x.direct("google.com", x.google.idToken(googleClient, map[string]any{"iss": "https://evil.example"}), "", ""), "INVALID_IDP_RESPONSE")
	authErr(t, x.direct("apple.com", x.google.idToken(appleServices, nil), "", ""), "INVALID_IDP_RESPONSE") // Google-signed, Apple claimed
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	authErr(t, x.direct("google.com", signRS256(t, other, map[string]any{"iss": "https://accounts.google.com", "aud": googleClient, "sub": "g", "exp": time.Now().Unix() + 60}), "", ""), "INVALID_IDP_RESPONSE")
	authErr(t, x.direct("google.com", x.google.idToken(googleClient, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}), "", ""), "INVALID_IDP_RESPONSE")
	authErr(t, x.direct("google.com", "not.a.jwt", "", ""), "INVALID_IDP_RESPONSE")

	// Tamper: a Google ID token presented to Aruma's own endpoints.
	authErr(t, x.call(itk+"accounts:lookup", map[string]any{"idToken": tok}), "INVALID_ID_TOKEN")
	authErr(t, x.direct("google.com", tok, "", tok), "INVALID_ID_TOKEN")

	// A disabled provider.
	x.owner.mustOK(x.owner.do("PATCH", "/console/v1/projects/chat-app/auth/providers/google.com", map[string]any{"enabled": false}))
	authErr(t, x.direct("google.com", tok, "", ""), "OPERATION_NOT_ALLOWED")
	start := x.start("/emulator/auth/handler", "google.com", "signInViaPopup", "http://localhost:5173/app")
	assertEqual(t, "disabled page", start.data["errorCode"], "auth/operation-not-allowed")
}

func TestAuthIdPLinkingAndTakeover(t *testing.T) {
	x := newIdPEnv(t)
	// Someone registered the victim's address with a password and never verified it.
	squatter := ok(t, x.call(itk+"accounts:signUp", map[string]any{"email": "victim@gmail.com", "password": "squatter1", "returnSecureToken": true}))
	x.ageSessions()

	// The real owner signs in with Google, which vouches for the address.
	ev := x.flow("google.com", "signInViaPopup", googleClaims("g-victim", "victim@gmail.com", true))
	res := ok(t, x.signIn(ev, ""))
	assertEqual(t, "same account", res.str("localId"), squatter.str("localId"))
	assertEqual(t, "isNewUser", res.JSON["isNewUser"], false)
	authErr(t, x.call(itk+"accounts:signInWithPassword", map[string]any{"email": "victim@gmail.com", "password": "squatter1"}), "INVALID_LOGIN_CREDENTIALS")
	authErr(t, x.refresh(squatter.str("refreshToken")), "TOKEN_EXPIRED")
	user := ok(t, x.call(itk+"accounts:lookup", map[string]any{"idToken": res.str("idToken")})).list("users")[0]
	if user["emailVerified"] != true || user["passwordHash"] != nil || len(user["providerUserInfo"].([]any)) != 1 || user["displayName"] != "Sara Ali" {
		t.Fatalf("after takeover protection: %v", user)
	}

	// A verified password account keeps its password when Google is added by email match.
	pw := ok(t, x.call(itk+"accounts:signUp", map[string]any{"email": "kept@gmail.com", "password": "secret12", "returnSecureToken": true}))
	if _, err := x.env.Pool.Exec(context.Background(), `UPDATE auth.users SET email_verified = true WHERE uid = $1`, pw.str("localId")); err != nil {
		t.Fatal(err)
	}
	ok(t, x.direct("google.com", x.google.idToken(googleClient, googleClaims("g-kept", "kept@gmail.com", true)), "", ""))
	ok(t, x.call(itk+"accounts:signInWithPassword", map[string]any{"email": "kept@gmail.com", "password": "secret12"}))

	// Linking: a password user adds Google (linkViaPopup passes the user's idToken).
	linker := ok(t, x.call(itk+"accounts:signUp", map[string]any{"email": "nour@example.com", "password": "secret12", "returnSecureToken": true}))
	ev = x.flow("google.com", "linkViaPopup", googleClaims("g-nour", "nour@example.com", true))
	linked := ok(t, x.signIn(ev, linker.str("idToken")))
	assertEqual(t, "uid kept", linked.str("localId"), linker.str("localId"))
	user = ok(t, x.call(itk+"accounts:lookup", map[string]any{"idToken": linked.str("idToken")})).list("users")[0]
	if len(user["providerUserInfo"].([]any)) != 2 || user["emailVerified"] != true {
		t.Fatalf("after link: %v", user)
	}

	// Tamper: the same Google identity linked to another user. With returnIdpCredential the
	// error comes in a 200 body with the credential, as the SDKs expect.
	other := ok(t, x.call(itk+"accounts:signUp", map[string]any{"email": "other@example.com", "password": "secret12", "returnSecureToken": true}))
	taken := x.direct("google.com", x.google.idToken(googleClient, googleClaims("g-nour", "nour@example.com", true)), "", other.str("idToken"))
	if taken.Status != 200 || taken.str("errorMessage") != "FEDERATED_USER_ID_ALREADY_LINKED" || taken.str("oauthIdToken") == "" || taken.str("idToken") != "" {
		t.Fatalf("already linked: %d %s", taken.Status, taken.Body)
	}
	plain := x.call(itk+"accounts:signInWithIdp", map[string]any{
		"requestUri": "http://localhost", "idToken": other.str("idToken"), "returnSecureToken": true,
		"postBody": url.Values{"id_token": {x.google.idToken(googleClient, googleClaims("g-nour", "nour@example.com", true))}, "providerId": {"google.com"}}.Encode(),
	})
	authErr(t, plain, "FEDERATED_USER_ID_ALREADY_LINKED")
	// Linking an identity whose email belongs to someone else.
	email := x.direct("google.com", x.google.idToken(googleClient, googleClaims("g-new", "kept@gmail.com", true)), "", other.str("idToken"))
	assertEqual(t, "email taken", email.str("errorMessage"), "EMAIL_EXISTS")
}

func TestAuthIdPAccountExistsAndReauth(t *testing.T) {
	x := newIdPEnv(t)
	existing := ok(t, x.call(itk+"accounts:signUp", map[string]any{"email": "maha@example.com", "password": "secret12", "returnSecureToken": true}))
	// The provider does not vouch for the email: the user must confirm by signing in first.
	unverified := map[string]any{"sub": "apple-maha", "email": "maha@example.com", "email_verified": "false"}
	res := ok(t, x.direct("apple.com", x.apple.idToken(appleServices, unverified), "", ""))
	if res.JSON["needConfirmation"] != true || res.str("idToken") != "" || res.str("localId") != "" || res.JSON["verifiedProvider"] != nil || res.str("oauthIdToken") == "" {
		t.Fatalf("needConfirmation response: %s", res.Body)
	}
	// Without enumeration protection the existing providers are named, as Firebase does.
	if _, err := x.env.Pool.Exec(context.Background(), `UPDATE auth.project_config SET enumeration_protection = false WHERE project_number = $1`, x.number); err != nil {
		t.Fatal(err)
	}
	if _, err := x.env.Pool.Exec(context.Background(), `INSERT INTO auth.user_providers (project_number, uid, provider_id, raw_id) VALUES ($1, $2, 'google.com', 'g-maha')`, x.number, existing.str("localId")); err != nil {
		t.Fatal(err)
	}
	res = ok(t, x.direct("apple.com", x.apple.idToken(appleServices, unverified), "", ""))
	if list, _ := res.JSON["verifiedProvider"].([]any); len(list) != 1 || list[0] != "google.com" {
		t.Fatalf("verifiedProvider: %s", res.Body)
	}

	// Re-authentication with a Google account that has no Aruma account creates nothing.
	ev := x.flow("google.com", "reauthViaPopup", googleClaims("g-stranger", "stranger@gmail.com", true))
	authErr(t, x.signIn(ev, ""), "USER_NOT_FOUND")
	ev = x.flow("google.com", "reauthViaPopup", googleClaims("g-maha", "maha@example.com", true))
	assertEqual(t, "reauth", ok(t, x.signIn(ev, "")).str("localId"), existing.str("localId"))
}

func TestAuthIdPAnonymousUpgrade(t *testing.T) {
	x := newIdPEnv(t)
	x.enableAnonymous()
	anon := ok(t, x.call(itk+"accounts:signUp", map[string]any{"returnSecureToken": true}))
	ev := x.flow("google.com", "linkViaRedirect", googleClaims("g-anon", "anon@gmail.com", true))
	up := ok(t, x.signIn(ev, anon.str("idToken")))
	assertEqual(t, "uid kept", up.str("localId"), anon.str("localId"))
	c := claims(t, up.str("idToken"))
	assertEqual(t, "provider", c["firebase"].(map[string]any)["sign_in_provider"], "google.com")
	assertEqual(t, "email", c["email"], "anon@gmail.com")
}

func TestAuthIdPStateTampering(t *testing.T) {
	x := newIdPEnv(t)
	a := x.flow("google.com", "signInViaPopup", googleClaims("g-a", "a@gmail.com", true))
	b := x.flow("google.com", "signInViaPopup", googleClaims("g-b", "b@gmail.com", true))
	// Swapped: A's callback with B's sessionId. A is burnt by the attempt; B still works.
	authErr(t, x.signIn(authEvent{requestURI: a.requestURI, sessionID: b.sessionID}, ""), "INVALID_IDP_RESPONSE")
	authErr(t, x.signIn(a, ""), "INVALID_IDP_RESPONSE")
	ok(t, x.signIn(b, ""))
	// No sessionId at all.
	c := x.flow("google.com", "signInViaPopup", googleClaims("g-c", "c@gmail.com", true))
	authErr(t, x.signIn(authEvent{requestURI: c.requestURI}, ""), "INVALID_IDP_RESPONSE")

	// Wrong nonce: the provider's token carries another nonce.
	d := x.flow("google.com", "signInViaPopup", map[string]any{"sub": "g-d", "nonce": sha256Hex("not-ours")})
	authErr(t, x.signIn(d, ""), "MISSING_OR_INVALID_NONCE")
	// A token for another client, through the code flow.
	e := x.flow("google.com", "signInViaPopup", map[string]any{"sub": "g-e", "aud": "other-client.apps.googleusercontent.com"})
	authErr(t, x.signIn(e, ""), "INVALID_IDP_RESPONSE")
	// The callback answered on another host than the one the provider redirected to.
	f := x.flow("google.com", "signInViaPopup", googleClaims("g-f", "f@gmail.com", true))
	moved := strings.Replace(f.requestURI, x.env.URL, "https://evil.example", 1)
	authErr(t, x.signIn(authEvent{requestURI: moved, sessionID: f.sessionID}, ""), "UNAUTHORIZED_DOMAIN")
	// An unknown state renders an error page and nothing to deliver.
	resp, err := http.Get(x.env.URL + "/__/auth/handler?state=forged&code=x")
	pg := readPage(t, resp, err)
	if pg.status != 400 || pg.data["mode"] != "error" || pg.data["authType"] != "" {
		t.Fatalf("forged state page: %d %v", pg.status, pg.data)
	}
}

func TestAuthIdPUnauthorizedDomains(t *testing.T) {
	x := newIdPEnv(t)
	for _, redirect := range []string{"https://evil.example/app", "javascript:alert(1)", "http://localhost.evil.example/"} {
		pg := x.start("/emulator/auth/handler", "google.com", "signInViaRedirect", redirect)
		if pg.status != 403 || pg.data["mode"] != "error" || pg.data["redirectUrl"] != "" || pg.data["authUri"] != "" || pg.data["reason"] != "domain" {
			t.Fatalf("redirect to %s: %d %v", redirect, pg.status, pg.data)
		}
	}
	authErr(t, x.call(itk+"accounts:createAuthUri", map[string]any{"providerId": "google.com", "continueUri": "https://evil.example/cb"}), "UNAUTHORIZED_DOMAIN")
	authErr(t, x.call(itk+"accounts:createAuthUri", map[string]any{"providerId": "google.com"}), "MISSING_CONTINUE_URI")

	// createAuthUri on an authorized domain starts a flow redeemable with its sessionId.
	started := ok(t, x.call(itk+"accounts:createAuthUri", map[string]any{"providerId": "google.com", "continueUri": "http://localhost:5173/callback", "oauthScope": "https://www.googleapis.com/auth/drive.file"}))
	assertEqual(t, "kind", started.str("kind"), "identitytoolkit#CreateAuthUriResponse")
	if !strings.Contains(started.str("authUri"), "drive.file") {
		t.Fatalf("scope missing: %s", started.str("authUri"))
	}
	code, state := x.google.authorize(started.str("authUri"), googleClaims("g-cau", "cau@gmail.com", true))
	ok(t, x.signIn(authEvent{requestURI: "http://localhost:5173/callback?state=" + state + "&code=" + code, sessionID: started.str("sessionId")}, ""))

	// Only the handler and iframe exist under /emulator/.
	for _, path := range []string{"/emulator/v1/projects/chat-app/accounts", "/emulator/auth/config", "/emulator/action"} {
		resp, err := http.Get(x.env.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("%s: %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestAuthIdPIframeAndInitJSON(t *testing.T) {
	x := newIdPEnv(t)
	resp, err := http.Get(x.env.URL + "/emulator/auth/iframe?apiKey=" + x.key + "&appName=%5BDEFAULT%5D&v=13.0.0&eid=p")
	pg := readPage(t, resp, err)
	csp := pg.header.Get("Content-Security-Policy")
	if pg.status != 200 || pg.header.Get("X-Frame-Options") != "" ||
		!strings.Contains(csp, "frame-ancestors ") || !strings.Contains(csp, " http://localhost:* https://localhost:*") || !strings.Contains(csp, "https://apis.google.com") {
		t.Fatalf("iframe page %d: %v", pg.status, pg.header)
	}
	assertScriptHashes(t, pg)
	resp, err = http.Get(x.env.URL + "/__/auth/iframe")
	if pg = readPage(t, resp, err); !strings.Contains(pg.header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("iframe without a key may not be framed: %v", pg.header)
	}

	// init.json is served on a project's own authDomain only.
	resp, err = http.Get(x.env.URL + "/__/firebase/init.json")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	assertEqual(t, "shared domain", resp.StatusCode, 404)
	x.owner.mustOK(x.owner.do("PATCH", "/console/v1/projects/chat-app", map[string]any{"authDomain": "auth.chat.example"}))
	req, _ := http.NewRequest("GET", x.env.URL+"/__/firebase/init.json", nil)
	req.Host = "auth.chat.example"
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeResponse(t, resp)
	if cfg.Status != 200 || cfg.str("apiKey") != x.key || cfg.str("projectId") != "chat-app" || cfg.str("authDomain") != "auth.chat.example" {
		t.Fatalf("init.json: %d %s", cfg.Status, cfg.Body)
	}
	providers := x.owner.mustOK(x.owner.do("GET", "/console/v1/projects/chat-app/auth/providers", nil))
	assertEqual(t, "redirectUri on the own domain", providers.str("redirectUri"), "https://auth.chat.example/__/auth/handler")
}

// assertScriptHashes checks that the page's CSP allows exactly its inline scripts and styles.
func assertScriptHashes(t *testing.T, pg page) {
	t.Helper()
	csp := pg.header.Get("Content-Security-Policy")
	for _, re := range []*regexp.Regexp{regexp.MustCompile(`(?s)<script>(.*?)</script>`), regexp.MustCompile(`(?s)<style>(.*?)</style>`)} {
		for _, m := range re.FindAllStringSubmatch(pg.html, -1) {
			sum := sha256.Sum256([]byte(m[1]))
			if !strings.Contains(csp, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'") {
				t.Fatalf("CSP %q does not allow an inline block of the page", csp)
			}
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Fatalf("CSP: %s", csp)
	}
}

func TestAuthIdPConsoleProviders(t *testing.T) {
	e := newEnv(t, nil)
	p := e.authProject("chat-app")
	base := "/console/v1/projects/chat-app/auth/providers"
	got := p.owner.mustOK(p.owner.do("GET", base, nil))
	assertEqual(t, "redirectUri", got.str("redirectUri"), e.URL+"/__/auth/handler")
	if string(mustJSON(got.JSON["providers"])) != `[{"clientId":"","clientSecretSet":false,"enabled":false,"providerId":"google.com"},{"enabled":false,"keyId":"","privateKeySet":false,"providerId":"apple.com","servicesId":"","teamId":""}]` {
		t.Fatalf("providers: %s", got.Body)
	}

	expect(t, p.owner.do("PATCH", base+"/google.com", map[string]any{"enabled": true}), 400, "INVALID_ARGUMENT")
	g := p.owner.mustOK(p.owner.do("PATCH", base+"/google.com", map[string]any{"clientId": googleClient, "clientSecret": googleSecret, "enabled": true}))
	if string(mustJSON(g.JSON)) != `{"clientId":"`+googleClient+`","clientSecretSet":true,"enabled":true,"providerId":"google.com"}` {
		t.Fatalf("google: %s", g.Body)
	}
	expect(t, p.owner.do("PATCH", base+"/google.com", map[string]any{"servicesId": "x"}), 400, "INVALID_ARGUMENT")
	expect(t, p.owner.do("PATCH", base+"/apple.com", map[string]any{"privateKey": "-----BEGIN PRIVATE KEY-----\nnope\n-----END PRIVATE KEY-----"}), 400, "INVALID_ARGUMENT")
	expect(t, p.owner.do("PATCH", base+"/apple.com", map[string]any{"teamId": "short"}), 400, "INVALID_ARGUMENT")
	expect(t, p.owner.do("PATCH", base+"/apple.com", map[string]any{"clientSecret": "x"}), 400, "INVALID_ARGUMENT")
	expect(t, p.owner.do("PATCH", base+"/facebook.com", map[string]any{"enabled": true}), 404, "NOT_FOUND")

	ecKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(ecKey)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	a := p.owner.mustOK(p.owner.do("PATCH", base+"/apple.com", map[string]any{"servicesId": appleServices, "teamId": appleTeam, "keyId": appleKeyID, "privateKey": keyPEM, "enabled": true}))
	assertEqual(t, "privateKeySet", a.JSON["privateKeySet"], true)
	if strings.Contains(string(a.Body), "PRIVATE") {
		t.Fatal("the private key came back")
	}

	// Another account cannot see or change the project's providers.
	stranger := e.account("stranger@example.com")
	expect(t, stranger.do("GET", base, nil), 404, "NOT_FOUND")

	// Zero raw secrets in the database.
	var dump string
	if err := e.Pool.QueryRow(context.Background(), `SELECT string_agg(t::text, ' ') FROM auth.idp_configs t`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dump, googleSecret) || strings.Contains(dump, "PRIVATE KEY") || strings.Contains(dump, hex.EncodeToString([]byte(googleSecret))) {
		t.Fatal("a provider secret is stored in the clear")
	}
}
