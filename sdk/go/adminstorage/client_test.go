// Copyright 2026 The Aruma Authors
// SPDX-License-Identifier: Apache-2.0

package adminstorage

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/storage"
)

// testKey is a throwaway service account key, made for the run: never a real one.
var testKey = sync.OnceValue(func() string {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		panic(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
})

// keyJSON is a key file as Aruma issues it, with extra fields merged in.
func keyJSON(t *testing.T, tokenURI string, extra map[string]string) []byte {
	t.Helper()
	m := map[string]string{
		"type": "service_account", "project_id": "demo-app", "private_key_id": "k1", "private_key": testKey(),
		"client_email": "backend@demo-app.sa.example.com", "client_id": "1", "token_uri": tokenURI,
	}
	for k, v := range extra {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestEmulatorGuard(t *testing.T) {
	if err := emulatorGuard(env(nil)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"STORAGE_EMULATOR_HOST", "FIREBASE_STORAGE_EMULATOR_HOST"} {
		err := emulatorGuard(env(map[string]string{name: "127.0.0.1:9199"}))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

func TestNewClientRefusesTheEmulatorVariables(t *testing.T) {
	for _, name := range []string{"STORAGE_EMULATOR_HOST", "FIREBASE_STORAGE_EMULATOR_HOST"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "127.0.0.1:9199")
			_, err := NewClient(t.Context(), WithCredentialsJSON(keyJSON(t, "https://aruma.example.com/oauth2/v1/token", nil)))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("got %v, want a refusal naming %s", err, name)
			}
		})
	}
}

func TestResolveURL(t *testing.T) {
	key := serviceAccountKey{TokenURI: "https://Aruma.Example.com/oauth2/v1/token"}
	for _, tc := range []struct {
		opt, env string
		key      serviceAccountKey
		want     string
	}{
		{key: key, want: "https://aruma.example.com"},
		{env: "https://env.example.com/", key: key, want: "https://env.example.com"},
		{opt: "https://opt.example.com", env: "https://env.example.com", key: key, want: "https://opt.example.com"},
		{opt: "https://aruma.example.com:443", want: "https://aruma.example.com"},
		{opt: "https://aruma.example.com:8443", want: "https://aruma.example.com:8443"},
		{opt: "http://127.0.0.1:9350", want: "http://127.0.0.1:9350"},
		{opt: "http://localhost:80/", want: "http://localhost"},
		{opt: "http://[::1]:9350", want: "http://[::1]:9350"},
	} {
		got, err := resolveURL(tc.opt, tc.key, env(map[string]string{"ARUMA_URL": tc.env}))
		if err != nil || got != tc.want {
			t.Errorf("resolveURL(%q, ARUMA_URL=%q, %q) = %q, %v; want %q", tc.opt, tc.env, tc.key.TokenURI, got, err, tc.want)
		}
	}
	for _, bad := range []string{
		"http://aruma.example.com", "https://storage.googleapis.com", "https://firebasestorage.googleapis.com",
		"https://accounts.google.com", "https://aruma.example.com/base", "https://aruma.example.com/?x=1",
		"https://user@aruma.example.com", "https://aruma.example.com/#f", "not a url",
	} {
		if got, err := resolveURL(bad, serviceAccountKey{}, env(nil)); err == nil {
			t.Errorf("resolveURL(%q) = %q, want an error", bad, got)
		}
	}
	// A Google key file has a Google token_uri: refused, never followed.
	if _, err := resolveURL("", serviceAccountKey{TokenURI: "https://oauth2.googleapis.com/token"}, env(nil)); err == nil {
		t.Error("a Google token_uri was accepted")
	}
	if _, err := resolveURL("", serviceAccountKey{}, env(nil)); err == nil || !strings.Contains(err.Error(), "ARUMA_URL") {
		t.Errorf("no URL at all: %v", err)
	}
}

func TestLoadKey(t *testing.T) {
	raw := keyJSON(t, "https://aruma.example.com/oauth2/v1/token", map[string]string{"universe_domain": "aruma.example.com"})
	k, err := loadKey(settings{keyJSON: raw}, env(nil))
	if err != nil || k.ClientEmail != "backend@demo-app.sa.example.com" || k.PrivateKeyID != "k1" {
		t.Fatalf("got %+v, %v", k, err)
	}
	if _, err := loadKey(settings{}, env(map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "/nonexistent/key.json"})); err == nil {
		t.Error("a missing key file was accepted")
	}
	if _, err := loadKey(settings{}, env(nil)); err == nil || !strings.Contains(err.Error(), "GOOGLE_APPLICATION_CREDENTIALS") {
		t.Errorf("no key at all: %v", err)
	}
	if _, err := loadKey(settings{keyJSON: []byte(`{"client_email":"a@b.c"}`)}, env(nil)); err == nil {
		t.Error("a key without private_key was accepted")
	}
}

func TestBuckets(t *testing.T) {
	c, err := NewClient(t.Context(), WithCredentialsJSON(keyJSON(t, "https://aruma.example.com/oauth2/v1/token", nil)),
		WithDefaultBucket("demo-app.aruma.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.URL() != "https://aruma.example.com" {
		t.Errorf("URL() = %q", c.URL())
	}
	if b, err := c.DefaultBucket(); err != nil || b.BucketName() != "demo-app.aruma.example.com" {
		t.Errorf("DefaultBucket() = %v, %v", b, err)
	}
	if b, err := c.Bucket("other"); err != nil || b.BucketName() != "other" {
		t.Errorf("Bucket(other) = %v, %v", b, err)
	}
	if _, err := c.Bucket(""); err == nil {
		t.Error("an empty bucket name was accepted")
	}
	if _, err := c.SignedURL("b", "o", &storage.SignedURLOptions{Method: http.MethodGet}); !errors.Is(err, ErrSignedURLsUnsupported) {
		t.Errorf("SignedURL: %v", err)
	}
	none, err := NewClient(t.Context(), WithCredentialsJSON(keyJSON(t, "https://aruma.example.com/oauth2/v1/token", nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := none.DefaultBucket(); err == nil {
		t.Error("DefaultBucket without WithDefaultBucket returned a handle")
	}
}

// The token goes to the Aruma origin and nowhere else, whatever asks for another host.
func TestOtherOriginsAreRefused(t *testing.T) {
	var sent []string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		sent = append(sent, r.URL.String())
		return nil, errors.New("unreachable")
	})
	ts, err := newTokenSource("https://aruma.example.com", serviceAccountKey{ClientEmail: "a@b.c", PrivateKey: testKey()}, rt)
	if err != nil {
		t.Fatal(err)
	}
	hc := authorizedClient("https://aruma.example.com", ts, nil, rt)
	for _, u := range []string{
		"https://firebasestorage.googleapis.com/v0/b/demo/o/x", "https://storage.googleapis.com/upload/storage/v1/b/demo/o",
		"http://aruma.example.com/storage/v1/b/demo", "https://aruma.example.com.evil.test/v0/b/demo/o/x",
		"https://aruma.example.com:8443/storage/v1/b/demo",
	} {
		resp, err := hc.Get(u)
		if err == nil {
			resp.Body.Close()
			t.Errorf("GET %s went through", u)
		} else if !strings.Contains(err.Error(), "refusing a request") {
			t.Errorf("GET %s: %v", u, err)
		}
	}
	if len(sent) != 0 {
		t.Errorf("requests reached the transport: %v", sent)
	}
}

func TestEscapeComponent(t *testing.T) {
	// The values are encodeURIComponent's in Node 24.
	for in, want := range map[string]string{
		"photos/cat.png":  "photos%2Fcat.png",
		"a b+c&d=e?f#g":   "a%20b%2Bc%26d%3De%3Ff%23g",
		"-_.!~*'()":       "-_.!~*'()",
		"صور/ü 1.jpg":     "%D8%B5%D9%88%D8%B1%2F%C3%BC%201.jpg",
		"x:y@z,$;%":       "x%3Ay%40z%2C%24%3B%25",
		"[]{}|\\^`\"<>\t": "%5B%5D%7B%7D%7C%5C%5E%60%22%3C%3E%09",
	} {
		if got := escapeComponent(in); got != want {
			t.Errorf("escapeComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
