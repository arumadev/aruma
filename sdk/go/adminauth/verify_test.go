// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminauth

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"firebase.google.com/go/v4/auth"
)

// issuer is a local Aruma-like issuer with its own keys: one for ID tokens, one for cookies.
type issuer struct {
	srv          *httptest.Server
	idKey, ckKey *rsa.PrivateKey
	discIssuer   string // overrides the discovery document's issuer
	jwksURI      string // overrides its jwks_uri
	jwksRedirect string // answers the key set with a redirect here
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
	is := &issuer{}
	var err error
	if is.idKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	if is.ckKey, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		t.Fatal(err)
	}
	is.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		project, file, _ := strings.Cut(rest, "/.well-known/")
		iss := is.srv.URL + "/" + prefix + "/" + project
		w.Header().Set("Cache-Control", "public, max-age=300")
		switch file {
		case "openid-configuration":
			doc := map[string]string{"issuer": iss, "jwks_uri": iss + "/.well-known/jwks.json"}
			if is.discIssuer != "" {
				doc["issuer"] = is.discIssuer
			}
			if is.jwksURI != "" {
				doc["jwks_uri"] = is.jwksURI
			}
			_ = json.NewEncoder(w).Encode(doc)
		case "jwks.json":
			if is.jwksRedirect != "" {
				http.Redirect(w, r, is.jwksRedirect, http.StatusFound)
				return
			}
			k := &is.idKey.PublicKey
			if prefix == "session-issuers" {
				k = &is.ckKey.PublicKey
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "key-1",
				"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes()),
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(is.srv.Close)
	return is
}

func b64(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func signRS256(key *rsa.PrivateKey, header, claims map[string]any) string {
	signing := b64(header) + "." + b64(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (is *issuer) token(prefix string, key *rsa.PrivateKey, header, claims map[string]any) string {
	now := time.Now().Unix()
	h := map[string]any{"alg": "RS256", "kid": "key-1", "typ": "JWT"}
	c := map[string]any{"iss": is.srv.URL + prefix + "demo-app", "aud": "demo-app", "sub": "user-1", "iat": now, "exp": now + 3600, "auth_time": now,
		"firebase": map[string]any{"sign_in_provider": "password", "identities": map[string]any{"email": []string{"a@example.com"}}}}
	for k, v := range header {
		if v == nil {
			delete(h, k)
		} else {
			h[k] = v
		}
	}
	for k, v := range claims {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return signRS256(key, h, c)
}

func TestVerifierAcceptsGenuineTokens(t *testing.T) {
	is := newIssuer(t)
	v := newVerifier(is.srv.URL, "demo-app", idTokenKind, http.DefaultClient)
	tok, err := v.verify(context.Background(), is.token("/issuers/", is.idKey, nil, map[string]any{"role": "admin"}))
	if err != nil {
		t.Fatal(err)
	}
	if tok.UID != "user-1" || tok.Subject != "user-1" || tok.Claims["role"] != "admin" || tok.Firebase.SignInProvider != "password" || tok.AuthTime == 0 {
		t.Fatalf("token: %+v", tok)
	}
	if _, ok := tok.Claims["iss"]; ok {
		t.Fatal("standard claims must not be in Claims, as in firebase-admin-go")
	}
	// firebase-admin-go allows 300 seconds of skew on exp.
	now := time.Now().Unix()
	if _, err := v.verify(context.Background(), is.token("/issuers/", is.idKey, nil, map[string]any{"iat": now - 3700, "exp": now - 100})); err != nil {
		t.Fatalf("within skew: %v", err)
	}
	expired := is.token("/issuers/", is.idKey, nil, map[string]any{"iat": now - 7200, "exp": now - 3600})
	if _, err := v.verify(context.Background(), expired); !auth.IsIDTokenExpired(err) {
		t.Fatalf("expired: %v", err)
	}
}

func TestVerifierRejectsForgedTokens(t *testing.T) {
	is := newIssuer(t)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	v := newVerifier(is.srv.URL, "demo-app", idTokenKind, http.DefaultClient)
	now := time.Now().Unix()
	good := is.token("/issuers/", is.idKey, nil, nil)
	parts := strings.Split(good, ".")
	pubDER, _ := x509.MarshalPKIXPublicKey(&is.idKey.PublicKey)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	hsHeader := b64(map[string]string{"alg": "HS256", "kid": "key-1"})
	mac := hmac.New(sha256.New, pubPEM)
	mac.Write([]byte(hsHeader + "." + parts[1]))

	for name, tok := range map[string]string{
		"alg none":         b64(map[string]string{"alg": "none", "kid": "key-1"}) + "." + parts[1] + ".",
		"unsigned":         parts[0] + "." + parts[1] + ".",
		"HS256 confusion":  hsHeader + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
		"no kid":           is.token("/issuers/", is.idKey, map[string]any{"kid": nil}, nil),
		"unknown kid":      is.token("/issuers/", is.idKey, map[string]any{"kid": "nope"}, nil),
		"other key":        is.token("/issuers/", other, nil, nil),
		"modified payload": parts[0] + "." + b64(map[string]any{"iss": is.srv.URL + "/issuers/demo-app", "aud": "demo-app", "sub": "admin", "iat": now, "exp": now + 3600, "auth_time": now}) + "." + parts[2],
		"other project":    is.token("/issuers/", is.idKey, nil, map[string]any{"aud": "other-app"}),
		"Google issuer":    is.token("/issuers/", is.idKey, nil, map[string]any{"iss": "https://securetoken.google.com/demo-app"}),
		"session cookie":   is.token("/session-issuers/", is.ckKey, nil, nil),
		"empty sub":        is.token("/issuers/", is.idKey, nil, map[string]any{"sub": ""}),
		"long sub":         is.token("/issuers/", is.idKey, nil, map[string]any{"sub": strings.Repeat("x", 129)}),
		"future iat":       is.token("/issuers/", is.idKey, nil, map[string]any{"iat": now + 3600}),
		"no auth_time":     is.token("/issuers/", is.idKey, nil, map[string]any{"auth_time": nil}),
		"not a JWT":        "abc.def",
		"empty":            "",
		"custom token":     b64(map[string]string{"alg": "RS256"}) + "." + b64(map[string]string{"aud": customTokenAud}) + ".x",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := v.verify(context.Background(), tok)
			if !auth.IsIDTokenInvalid(err) || auth.IsIDTokenExpired(err) {
				t.Fatalf("want ID_TOKEN_INVALID, got %v", err)
			}
		})
	}
}

func TestSessionCookieVerifier(t *testing.T) {
	is := newIssuer(t)
	v := newVerifier(is.srv.URL, "demo-app", sessionCookieKind, http.DefaultClient)
	if _, err := v.verify(context.Background(), is.token("/session-issuers/", is.ckKey, nil, nil)); err != nil {
		t.Fatal(err)
	}
	_, err := v.verify(context.Background(), is.token("/issuers/", is.idKey, nil, nil))
	if !auth.IsSessionCookieInvalid(err) || auth.IsIDTokenInvalid(err) {
		t.Fatalf("an ID token as a cookie: %v", err)
	}
	now := time.Now().Unix()
	_, err = v.verify(context.Background(), is.token("/session-issuers/", is.ckKey, nil, map[string]any{"iat": now - 7200, "exp": now - 3600}))
	if !auth.IsSessionCookieExpired(err) {
		t.Fatalf("expired cookie: %v", err)
	}
}

func TestVerifierTrustsOnlyItsServer(t *testing.T) {
	if _, err := firebaseErrorType(); err != nil { // as NewClient does, so the codes are official
		t.Fatal(err)
	}
	is := newIssuer(t)
	tok := is.token("/issuers/", is.idKey, nil, nil)
	// Another server publishing kid key-1 with its own key, and a token signed by that key.
	evil := newIssuer(t)
	forged := is.token("/issuers/", evil.ckKey, nil, nil)
	for name, c := range map[string]struct {
		set func()
		tok string
	}{
		"wrong issuer":       {func() { is.discIssuer = "https://evil.example/issuers/demo-app" }, tok},
		"keys elsewhere":     {func() { is.jwksURI = "https://evil.example/jwks.json" }, tok},
		"keys by a redirect": {func() { is.jwksRedirect = evil.srv.URL + "/session-issuers/demo-app/.well-known/jwks.json" }, forged},
	} {
		t.Run(name, func(t *testing.T) {
			c.set()
			defer func() { is.discIssuer, is.jwksURI, is.jwksRedirect = "", "", "" }()
			v := newVerifier(is.srv.URL, "demo-app", idTokenKind, defaultHTTPClient())
			if _, err := v.verify(context.Background(), c.tok); !auth.IsCertificateFetchFailed(err) {
				t.Fatalf("want CERTIFICATE_FETCH_FAILED, got %v", err)
			}
		})
	}
}
