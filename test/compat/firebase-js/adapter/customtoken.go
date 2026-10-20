package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// customTokenAudience is the audience the official createCustomToken writes.
const customTokenAudience = "https://identitytoolkit.googleapis.com/google.identity.identitytoolkit.v1.IdentityToolkit"

// signer is a service account key of the project, the credential createCustomToken signs with
// outside the emulator.
type signer struct {
	email string
	keyID string
	key   *rsa.PrivateKey
}

// newSigner creates a service account in the project and one key for it, kept in memory only.
// It dies with the throwaway server.
func (a *Adapter) newSigner(ctx context.Context) (*signer, error) {
	var sa struct {
		ID string `json:"id"`
	}
	if err := a.call(ctx, http.MethodPost, a.consolePath("serviceAccounts"), map[string]string{"accountId": "compat-adapter"}, &sa); err != nil {
		return nil, err
	}
	var file struct {
		ClientEmail  string `json:"client_email"`
		PrivateKeyID string `json:"private_key_id"`
		PrivateKey   string `json:"private_key"`
	}
	if err := a.call(ctx, http.MethodPost, a.consolePath("serviceAccounts/"+sa.ID+"/keys"), nil, &file); err != nil {
		return nil, err
	}
	block, _ := pem.Decode([]byte(file.PrivateKey))
	if block == nil {
		return nil, errors.New("the service account key file has no PEM private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("the service account key is %T, not RSA", parsed)
	}
	return &signer{email: file.ClientEmail, keyID: file.PrivateKeyID, key: key}, nil
}

// isCustomTokenSignIn reports whether path is the custom token sign-in of the v1 or v3 API.
func isCustomTokenSignIn(path string) bool {
	return strings.HasSuffix(path, "/v1/accounts:signInWithCustomToken") || strings.HasSuffix(path, "/relyingparty/verifyCustomToken")
}

// signCustomToken rewrites the body of a custom token sign-in whose token the emulator would
// accept but a real backend would not: a JSON object, or a JWT with "alg": "none", which is what
// the tests and firebase-admin without a service account (emulator mode) send. The new token
// carries the same uid, claims and tenant, signed as createCustomToken signs it. A signed token
// or anything unreadable is forwarded untouched, so Aruma judges it.
func (a *Adapter) signCustomToken(body []byte) []byte {
	var req map[string]any
	if a.signer == nil || json.Unmarshal(body, &req) != nil {
		return body
	}
	token, _ := req["token"].(string)
	payload, ok := unsignedPayload(token)
	if !ok {
		return body
	}
	signed, err := a.signer.sign(payload)
	if err != nil {
		return body
	}
	req["token"] = signed
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

// unsignedPayload returns the claims of an emulator-style custom token.
func unsignedPayload(token string) (map[string]any, bool) {
	var payload map[string]any
	if strings.HasPrefix(strings.TrimSpace(token), "{") {
		if json.Unmarshal([]byte(token), &payload) != nil {
			return nil, false
		}
		return payload, true
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	var header struct {
		Alg string `json:"alg"`
	}
	h, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(h, &header) != nil || header.Alg != "none" {
		return nil, false
	}
	p, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(p, &payload) != nil {
		return nil, false
	}
	return payload, true
}

// sign mints an RS256 custom token with the fields createCustomToken writes.
func (s *signer) sign(in map[string]any) (string, error) {
	now := time.Now().Unix()
	claims := map[string]any{"iss": s.email, "sub": s.email, "aud": customTokenAudience, "iat": now, "exp": now + 3600, "uid": in["uid"]}
	for _, k := range []string{"claims", "tenant_id"} {
		if v, ok := in[k]; ok {
			claims[k] = v
		}
	}
	enc := func(v any) (string, error) {
		b, err := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b), err
	}
	h, err := enc(map[string]string{"alg": "RS256", "typ": "JWT", "kid": s.keyID})
	if err != nil {
		return "", err
	}
	p, err := enc(claims)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(h + "." + p))
	sig, err := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return h + "." + p + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// readBody reads and closes a request body, up to 1 MiB.
func readBody(r io.ReadCloser) ([]byte, error) {
	defer r.Close()
	return io.ReadAll(io.LimitReader(r, 1<<20))
}

// replaceBody sets a forwarded request's body.
func replaceBody(req *http.Request, body []byte) {
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Del("Content-Length")
}
