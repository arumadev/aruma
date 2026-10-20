package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestSignCustomToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	a := &Adapter{signer: &signer{email: "compat-adapter@p.sa.test", keyID: "k1", key: key}}
	enc := func(v string) string { return base64.RawURLEncoding.EncodeToString([]byte(v)) }
	unsigned := enc(`{"alg":"none","typ":"JWT"}`) + "." + enc(`{"uid":"u1","claims":{"role":"x"}}`) + "."
	signed := enc(`{"alg":"RS256"}`) + "." + enc(`{"uid":"u1"}`) + ".c2ln"

	for name, tc := range map[string]struct {
		token string
		want  bool // rewritten
	}{
		"emulator JSON":  {`{"uid":"u1","claims":{"role":"x"}}`, true},
		"alg none JWT":   {unsigned, true},
		"signed JWT":     {signed, false},
		"garbage":        {"not a token", false},
		"empty":          {"", false},
		"JSON, no token": {"{", false},
	} {
		t.Run(name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"token": tc.token, "returnSecureToken": true})
			var out struct {
				Token             string `json:"token"`
				ReturnSecureToken bool   `json:"returnSecureToken"`
			}
			if err := json.Unmarshal(a.signCustomToken(body), &out); err != nil {
				t.Fatal(err)
			}
			if !out.ReturnSecureToken {
				t.Fatal("the other fields must be kept")
			}
			if (out.Token != tc.token) != tc.want {
				t.Fatalf("rewritten = %v, want %v", out.Token != tc.token, tc.want)
			}
			if !tc.want {
				return
			}
			parts := strings.Split(out.Token, ".")
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
			if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
				t.Fatal(err)
			}
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var c map[string]any
			_ = json.Unmarshal(raw, &c)
			if c["uid"] != "u1" || c["iss"] != "compat-adapter@p.sa.test" || c["sub"] != c["iss"] || c["aud"] != customTokenAudience ||
				c["claims"].(map[string]any)["role"] != "x" || c["exp"].(float64)-c["iat"].(float64) != 3600 {
				t.Fatalf("claims: %v", c)
			}
		})
	}
}
