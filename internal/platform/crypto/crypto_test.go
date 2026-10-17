package crypto

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func testKeys(t *testing.T) *Keys {
	t.Helper()
	k, err := NewKeys(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// Cheap parameters keep the tests fast; production uses DefaultArgon2.
var fast = Argon2Params{Memory: 64, Time: 1, Threads: 1}

func TestPasswordRoundTrip(t *testing.T) {
	ctx := context.Background()
	h := NewHasher(fast, 2)
	phc, err := h.Hash(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(phc, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Fatalf("unexpected PHC string %q", phc)
	}
	ok, rehash, err := h.Verify(ctx, "correct horse battery", phc)
	if err != nil || !ok || rehash {
		t.Fatalf("Verify = %v, %v, %v", ok, rehash, err)
	}
	ok, _, _ = h.Verify(ctx, "correct horse batterY", phc)
	if ok {
		t.Fatal("wrong password accepted")
	}
	other, _ := h.Hash(ctx, "correct horse battery")
	if other == phc {
		t.Fatal("salt is not random")
	}
}

func TestPasswordRehashOnNewParams(t *testing.T) {
	ctx := context.Background()
	phc, _ := NewHasher(fast, 1).Hash(ctx, "a long enough password")
	ok, rehash, err := NewHasher(Argon2Params{Memory: 128, Time: 1, Threads: 1}, 1).Verify(ctx, "a long enough password", phc)
	if err != nil || !ok || !rehash {
		t.Fatalf("Verify = %v, %v, %v; want ok and rehash", ok, rehash, err)
	}
}

func TestPasswordLengthLimits(t *testing.T) {
	ctx := context.Background()
	h := NewHasher(fast, 1)
	if _, err := h.Hash(ctx, "short"); err != ErrPasswordLength {
		t.Fatalf("short password: %v", err)
	}
	long := strings.Repeat("x", MaxPasswordLen+1)
	if _, err := h.Hash(ctx, long); err != ErrPasswordLength {
		t.Fatalf("long password: %v", err)
	}
	// A password longer than the limit never verifies, even against its own prefix.
	phc, _ := h.Hash(ctx, long[:MaxPasswordLen])
	if ok, _, _ := h.Verify(ctx, long, phc); ok {
		t.Fatal("over-long password verified by truncation")
	}
}

func TestMalformedHashesRejected(t *testing.T) {
	h := NewHasher(fast, 1)
	for _, phc := range []string{
		"", "plain", "$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=16$m=64,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		"$argon2id$v=19$m=64,t=1,p=1$!!$aGFzaGhhc2hoYXNoaGFzaA",
	} {
		if _, _, err := h.Verify(context.Background(), "whatever-password", phc); err == nil {
			t.Errorf("accepted malformed hash %q", phc)
		}
	}
}

func TestHasherBoundsConcurrency(t *testing.T) {
	h := NewHasher(fast, 1)
	h.sem <- struct{}{} // occupy the only slot
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := h.Hash(ctx, "password123"); err == nil {
		t.Fatal("hash ran beyond the concurrency limit")
	}
	<-h.sem
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); h.Hash(context.Background(), "password123") }()
	}
	wg.Wait()
}

func TestTokens(t *testing.T) {
	k := testKeys(t)
	a, b := NewToken("aruma_pat_"), NewToken("aruma_pat_")
	if a == b || !strings.HasPrefix(a, "aruma_pat_") || len(a) != len("aruma_pat_")+43 {
		t.Fatalf("bad tokens %q %q", a, b)
	}
	if !EqualHash(k.HashToken(a), k.HashToken(a)) || EqualHash(k.HashToken(a), k.HashToken(b)) {
		t.Fatal("hash is not a function of the token")
	}
	other, _ := NewKeys(bytes.Repeat([]byte{8}, 32))
	if EqualHash(k.HashToken(a), other.HashToken(a)) {
		t.Fatal("hash does not depend on the master key")
	}
}

func TestEnvelope(t *testing.T) {
	k := testKeys(t)
	secret := []byte("smtp-password")
	aad := []byte("control.smtp|password|row-1")
	env, err := k.Seal(secret, aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(env, secret) {
		t.Fatal("plaintext visible in envelope")
	}
	got, err := k.Open(env, aad)
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if _, err := k.Open(env, []byte("control.smtp|password|row-2")); err == nil {
		t.Fatal("envelope opened under another row's associated data")
	}
	tampered := bytes.Clone(env)
	tampered[len(tampered)-1] ^= 1
	if _, err := k.Open(tampered, aad); err == nil {
		t.Fatal("tampered envelope opened")
	}
	if _, err := k.Open(env[:5], aad); err == nil {
		t.Fatal("truncated envelope opened")
	}
}

func TestEnvelopeKEKRotation(t *testing.T) {
	k := testKeys(t)
	old, _ := k.Seal([]byte("v1 data"), []byte("x"))
	if err := k.AddKEK(2, bytes.Repeat([]byte{9}, 32)); err != nil {
		t.Fatal(err)
	}
	if got, err := k.Open(old, []byte("x")); err != nil || string(got) != "v1 data" {
		t.Fatalf("old envelope after rotation: %q, %v", got, err)
	}
	if !k.NeedsRewrap(old) {
		t.Fatal("old envelope should need rewrap")
	}
	fresh, _ := k.Seal([]byte("v2 data"), []byte("x"))
	if k.NeedsRewrap(fresh) {
		t.Fatal("fresh envelope should use the current KEK")
	}
	if err := k.AddKEK(2, bytes.Repeat([]byte{9}, 32)); err == nil {
		t.Fatal("KEK version must increase")
	}
}

func TestNewKeysRejectsShortMaster(t *testing.T) {
	if _, err := NewKeys(make([]byte, 16)); err == nil {
		t.Fatal("accepted a 16-byte master key")
	}
}
