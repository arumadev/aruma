package acceptance

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2/google"
)

type keyFile struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	ClientID     string `json:"client_id"`
	TokenURI     string `json:"token_uri"`
	X509URL      string `json:"client_x509_cert_url"`
}

func parseKeyFile(t *testing.T, b []byte) keyFile {
	t.Helper()
	var k keyFile
	if err := json.Unmarshal(b, &k); err != nil {
		t.Fatalf("key file: %v\n%s", err, b)
	}
	return k
}

// exchange gets an access token the way the Go Admin SDK does: golang.org/x/oauth2/google
// reading the key file, honouring its token_uri.
func exchange(t *testing.T, key []byte) (string, time.Time, error) {
	t.Helper()
	creds, err := google.CredentialsFromJSON(context.Background(), key, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		return "", time.Time{}, err
	}
	return tok.AccessToken, tok.Expiry, nil
}

// AC-17, AC-18: the key file is handed out once, its private half is never stored, and it
// buys a one-hour token that works on the Management API.
func TestServiceAccountKeyAndToken(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	c.project("chat-app")
	saID, raw := c.serviceAccountKey("chat-app", "backend")
	k := parseKeyFile(t, raw)
	if k.Type != "service_account" || k.ProjectID != "chat-app" || k.ClientEmail != "backend@chat-app.sa.127.0.0.1" ||
		len(k.PrivateKeyID) != 40 || len(k.ClientID) != 21 || k.TokenURI != e.URL+"/oauth2/v1/token" {
		t.Fatalf("key file: %s", raw)
	}
	block, _ := pem.Decode([]byte(k.PrivateKey))
	if block == nil || block.Type != "PRIVATE KEY" {
		t.Fatal("private key is not PKCS#8 PEM")
	}
	if _, err := x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
		t.Fatal(err)
	}

	// Only public records remain.
	keys := c.mustOK(c.do("GET", "/console/v1/projects/chat-app/serviceAccounts/"+saID+"/keys", nil))
	if strings.Contains(string(keys.Body), "PRIVATE") || len(keys.list("keys")) != 1 {
		t.Fatalf("key listing: %s", keys.Body)
	}
	assertNoSecretInDatabase(t, e, block.Bytes[len(block.Bytes)-40:])

	// The x509 URL serves the public certificate.
	resp, err := http.Get(k.X509URL)
	if err != nil {
		t.Fatal(err)
	}
	var certs map[string]string
	json.NewDecoder(resp.Body).Decode(&certs)
	resp.Body.Close()
	if !strings.Contains(certs[k.PrivateKeyID], "BEGIN CERTIFICATE") {
		t.Fatalf("x509 certs: %v", certs)
	}

	token, expiry, err := exchange(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "aruma_sat_") {
		t.Fatalf("token %q", token)
	}
	if left := time.Until(expiry); left < 55*time.Minute || left > 61*time.Minute {
		t.Fatalf("token lifetime %v, want one hour", left)
	}
	sa := e.withBearer(token)
	projects := sa.mustOK(sa.do("GET", "/v1beta1/projects", nil)).list("results")
	if len(projects) != 1 || projects[0]["projectId"] != "chat-app" {
		t.Fatalf("service account sees %v", projects)
	}
	// Service account tokens are not console credentials.
	expect(t, sa.do("GET", "/console/v1/me", nil), 401, "UNAUTHENTICATED")

	// Deleting the key voids new exchanges; deleting the account voids its tokens.
	c.mustOK(c.do("DELETE", "/console/v1/projects/chat-app/serviceAccounts/"+saID+"/keys/"+k.PrivateKeyID, nil))
	if _, _, err := exchange(t, raw); err == nil {
		t.Fatal("deleted key still exchanges")
	}
	c.mustOK(c.do("DELETE", "/console/v1/projects/chat-app/serviceAccounts/"+saID, nil))
	expect(t, sa.do("GET", "/v1beta1/projects", nil), 401, "UNAUTHENTICATED")
}

// assertNoSecretInDatabase dumps every text and bytea column and checks a fragment is absent.
func assertNoSecretInDatabase(t *testing.T, e *Env, fragment []byte) {
	t.Helper()
	rows, err := e.Pool.Query(context.Background(), `SELECT table_schema, table_name, column_name FROM information_schema.columns
		WHERE table_schema IN ('control', 'events') AND data_type IN ('text', 'bytea', 'jsonb')`)
	if err != nil {
		t.Fatal(err)
	}
	type col struct{ schema, table, column string }
	var cols []col
	for rows.Next() {
		var c col
		rows.Scan(&c.schema, &c.table, &c.column)
		cols = append(cols, c)
	}
	rows.Close()
	needle := base64.StdEncoding.EncodeToString(fragment)[:24]
	for _, c := range cols {
		var hits int
		q := `SELECT count(*) FROM "` + c.schema + `"."` + c.table + `" WHERE "` + c.column + `"::text LIKE '%' || $1 || '%' OR "` + c.column + `"::text LIKE '%PRIVATE KEY%'`
		if err := e.Pool.QueryRow(context.Background(), q, needle).Scan(&hits); err != nil {
			t.Fatal(err)
		}
		if hits > 0 {
			t.Fatalf("private key material found in %s.%s.%s", c.schema, c.table, c.column)
		}
	}
}

// signedAssertion builds a JWT bearer assertion by hand, for the negative cases.
func signedAssertion(t *testing.T, key keyFile, header, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signing := enc(header) + "." + enc(claims)
	block, _ := pem.Decode([]byte(key.PrivateKey))
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv.(*rsa.PrivateKey), crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func postAssertion(t *testing.T, e *Env, assertion string) (int, map[string]any) {
	t.Helper()
	resp, err := http.PostForm(e.URL+"/oauth2/v1/token", url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// RFC 7523 checks: algorithm, audience, lifetime and signature.
func TestTokenEndpointRejectsBadAssertions(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	c.project("chat-app")
	_, raw := c.serviceAccountKey("chat-app", "backend")
	k := parseKeyFile(t, raw)
	_, otherRaw := c.serviceAccountKey("chat-app", "another")
	other := parseKeyFile(t, otherRaw)

	now := time.Now().Unix()
	claims := func(mut func(map[string]any)) map[string]any {
		m := map[string]any{"iss": k.ClientEmail, "aud": k.TokenURI, "scope": "https://www.googleapis.com/auth/cloud-platform", "iat": now, "exp": now + 3600}
		if mut != nil {
			mut(m)
		}
		return m
	}
	rs256 := map[string]any{"alg": "RS256", "typ": "JWT"}

	// No kid, as Node's gtoken sends: still verified against the account's keys.
	if code, body := postAssertion(t, e, signedAssertion(t, k, rs256, claims(nil))); code != 200 || body["token_type"] != "Bearer" {
		t.Fatalf("valid assertion without kid: %d %v", code, body)
	}
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(mustJSON(claims(nil))) + "."
	cases := map[string]string{
		"alg none":        none,
		"HS256":           signedAssertion(t, k, map[string]any{"alg": "HS256"}, claims(nil)),
		"wrong audience":  signedAssertion(t, k, rs256, claims(func(m map[string]any) { m["aud"] = "https://oauth2.googleapis.com/token" })),
		"expired":         signedAssertion(t, k, rs256, claims(func(m map[string]any) { m["iat"] = now - 7200; m["exp"] = now - 3600 })),
		"too long":        signedAssertion(t, k, rs256, claims(func(m map[string]any) { m["exp"] = now + 7200 })),
		"future iat":      signedAssertion(t, k, rs256, claims(func(m map[string]any) { m["iat"] = now + 600; m["exp"] = now + 1200 })),
		"unknown issuer":  signedAssertion(t, k, rs256, claims(func(m map[string]any) { m["iss"] = "nobody@chat-app.sa.127.0.0.1" })),
		"signed by other": signedAssertion(t, other, rs256, claims(nil)),
		"wrong kid":       signedAssertion(t, k, map[string]any{"alg": "RS256", "kid": other.PrivateKeyID}, claims(nil)),
		"garbage":         "not.a.jwt",
	}
	for name, assertion := range cases {
		code, body := postAssertion(t, e, assertion)
		if code != 400 || body["error"] != "invalid_grant" {
			t.Errorf("%s: %d %v", name, code, body)
		}
	}
	resp, _ := http.PostForm(e.URL+"/oauth2/v1/token", url.Values{"grant_type": {"client_credentials"}})
	if resp.StatusCode != 400 {
		t.Errorf("unsupported grant: %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// The firebase CLI sends FIREBASE_TOKEN as a refresh token to FIREBASE_TOKEN_URL.
func TestRefreshTokenGrantForFirebaseCLI(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	pat := c.pat()
	post := func(tok string) (int, map[string]any) {
		resp, err := http.PostForm(e.URL+"/oauth2/v3/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok}, "client_id": {"x"}})
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}
	if code, body := post(pat); code != 200 || body["access_token"] != pat {
		t.Fatalf("refresh with CLI token: %d %v", code, body)
	}
	for _, bad := range []string{"aruma_pat_revoked-or-unknown", "1//google-refresh-token"} {
		if code, body := post(bad); code != 400 || body["error"] != "invalid_grant" {
			t.Errorf("refresh with %q: %d %v", bad, code, body)
		}
	}
}

func TestServiceAccountLimitsAndNames(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	c.project("chat-app")
	expect(t, c.do("POST", "/console/v1/projects/chat-app/serviceAccounts", map[string]any{"accountId": "Bad_Name"}), 400, "INVALID_ARGUMENT")
	sa := c.mustOK(c.do("POST", "/console/v1/projects/chat-app/serviceAccounts", map[string]any{"accountId": "backend"}))
	expect(t, c.do("POST", "/console/v1/projects/chat-app/serviceAccounts", map[string]any{"accountId": "backend"}), 409, "ALREADY_EXISTS")
	path := "/console/v1/projects/chat-app/serviceAccounts/" + sa.str("id") + "/keys"
	for i := 0; i < 10; i++ {
		c.mustOK(c.do("POST", path, nil))
	}
	expect(t, c.do("POST", path, nil), 400, "FAILED_PRECONDITION")
}
