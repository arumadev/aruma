package tokens

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/osama300/aurma-project/internal/platform/crypto"
	"github.com/osama300/aurma-project/internal/store/storetest"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	keys, err := crypto.NewKeys(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return NewManager(storetest.NewPool(t), keys)
}

const projA, projB = 482915736204, 582915736204

type testClaims struct {
	Sub string `json:"sub"`
}

func TestSignAndVerify(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	tok, err := m.Sign(ctx, projA, IDToken, testClaims{Sub: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := m.Verify(ctx, projA, IDToken, tok)
	if err != nil {
		t.Fatal(err)
	}
	var c testClaims
	json.Unmarshal(payload, &c)
	if c.Sub != "u1" {
		t.Fatalf("payload %s", payload)
	}
	var h Header
	hb, _ := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[0])
	json.Unmarshal(hb, &h)
	if h.Alg != "RS256" || len(h.Kid) != 40 || h.Typ != "JWT" {
		t.Fatalf("header %+v", h)
	}
}

// Tamper scenarios: unsigned, alg none, HMAC confusion, edited payload, another project's key,
// the other purpose.
func TestVerifyRejectsTampering(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	good, _ := m.Sign(ctx, projA, IDToken, testClaims{Sub: "u1"})
	parts := strings.Split(good, ".")
	enc := func(v string) string { return base64.RawURLEncoding.EncodeToString([]byte(v)) }
	var h Header
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	json.Unmarshal(hb, &h)

	jwks, _ := m.JWKS(ctx, projA, IDToken)
	pubN, _ := base64.RawURLEncoding.DecodeString(jwks[0].N)
	hsHeader := enc(`{"alg":"HS256","kid":"` + h.Kid + `"}`)
	mac := hmac.New(sha256.New, pubN) // the classic "public key as HMAC secret" confusion
	mac.Write([]byte(hsHeader + "." + parts[1]))

	cookie, _ := m.Sign(ctx, projA, SessionCookie, testClaims{Sub: "u1"})
	otherProject, _ := m.Sign(ctx, projB, IDToken, testClaims{Sub: "u1"})

	cases := map[string]struct {
		token   string
		project int64
		purpose Purpose
	}{
		"unsigned":        {parts[0] + "." + parts[1] + ".", projA, IDToken},
		"alg none":        {enc(`{"alg":"none","kid":"`+h.Kid+`"}`) + "." + parts[1] + ".", projA, IDToken},
		"hmac confusion":  {hsHeader + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), projA, IDToken},
		"edited payload":  {parts[0] + "." + enc(`{"sub":"admin"}`) + "." + parts[2], projA, IDToken},
		"no kid":          {enc(`{"alg":"RS256"}`) + "." + parts[1] + "." + parts[2], projA, IDToken},
		"other project":   {otherProject, projA, IDToken},
		"cookie as token": {cookie, projA, IDToken},
		"token as cookie": {good, projA, SessionCookie},
		"garbage":         {"a.b.c", projA, IDToken},
		"two segments":    {parts[0] + "." + parts[1], projA, IDToken},
	}
	for name, c := range cases {
		if _, err := m.Verify(ctx, c.project, c.purpose, c.token); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRotation(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	before, _ := m.Sign(ctx, projA, IDToken, testClaims{Sub: "u1"})
	jwks, _ := m.JWKS(ctx, projA, IDToken)
	if len(jwks) != 2 {
		t.Fatalf("want active + next published, got %d", len(jwks))
	}

	if err := m.Rotate(ctx, projA, IDToken, false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Verify(ctx, projA, IDToken, before); err != nil {
		t.Fatalf("token from the retired key no longer verifies: %v", err)
	}
	after, _ := m.Sign(ctx, projA, IDToken, testClaims{Sub: "u1"})
	if kid(before) == kid(after) {
		t.Fatal("rotation did not change the signing key")
	}
	if jwks, _ := m.JWKS(ctx, projA, IDToken); len(jwks) != 3 {
		t.Fatalf("want retired + active + next, got %d", len(jwks))
	}

	// Emergency: the active key is destroyed at once.
	if err := m.Rotate(ctx, projA, IDToken, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Verify(ctx, projA, IDToken, after); !errors.Is(err, ErrSignature) {
		t.Fatalf("token of the destroyed key: %v", err)
	}
	if _, err := m.Verify(ctx, projA, IDToken, before); err != nil {
		t.Fatalf("retired key should survive an emergency rotation of another key: %v", err)
	}

	// Maintenance destroys retired keys once their tokens have expired.
	m.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if err := m.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Verify(ctx, projA, IDToken, before); err == nil {
		t.Fatal("expired retired key still verifies")
	}
}

func TestConcurrentBootstrapMakesOneActiveKey(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	var wg sync.WaitGroup
	kids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Separate managers model separate request goroutines with cold caches.
			mm := &Manager{pool: m.pool, q: m.q, keys: m.keys, now: time.Now, signers: map[signerKey]*signer{}}
			tok, err := mm.Sign(ctx, projB, IDToken, testClaims{Sub: "x"})
			if err != nil {
				t.Error(err)
				return
			}
			kids <- kid(tok)
		}()
	}
	wg.Wait()
	close(kids)
	seen := map[string]bool{}
	for k := range kids {
		seen[k] = true
	}
	if len(seen) != 1 {
		t.Fatalf("%d different signing keys after concurrent bootstrap", len(seen))
	}
}

func TestPrivateKeysAreEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	m := newManager(t)
	m.Sign(ctx, projA, IDToken, testClaims{Sub: "u1"})
	var raw []byte
	if err := m.pool.QueryRow(ctx, `SELECT private_key_enc FROM auth.signing_keys LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte{0x30, 0x82}) && raw[0] == 0x30 {
		t.Fatal("private key stored as plain DER")
	}
	if raw[0] != 1 {
		t.Fatal("not an envelope")
	}
}

func kid(tok string) string {
	hb, _ := base64.RawURLEncoding.DecodeString(strings.Split(tok, ".")[0])
	var h Header
	json.Unmarshal(hb, &h)
	return h.Kid
}
