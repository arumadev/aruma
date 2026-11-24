package ops

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/url"
	"strings"
	"testing"
	"time"
)

// keyFile is a service account key file as the dashboard serves it.
type keyFile struct {
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
	ProjectID    string `json:"project_id"`
}

// saToken exchanges a signed assertion for an access token, as the Admin SDKs do.
func saToken(t *testing.T, a *api, raw []byte) (string, int) {
	t.Helper()
	var k keyFile
	if err := json.Unmarshal(raw, &k); err != nil {
		t.Fatalf("service account key: %v: %s", err, raw)
	}
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	now := time.Now().Unix()
	signing := enc(map[string]any{"alg": "RS256", "typ": "JWT", "kid": k.PrivateKeyID}) + "." +
		enc(map[string]any{"iss": k.ClientEmail, "aud": k.TokenURI, "scope": "https://www.googleapis.com/auth/cloud-platform", "iat": now, "exp": now + 3600})
	block, _ := pem.Decode([]byte(k.PrivateKey))
	if block == nil {
		t.Fatalf("service account key has no private key: %s", raw)
	}
	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv.(*rsa.PrivateKey), crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {signing + "." + base64.RawURLEncoding.EncodeToString(sig)}}
	r := a.do("POST", "/oauth2/v1/token", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, strings.NewReader(form.Encode()))
	return r.str("access_token"), r.Status
}
