package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"testing"
)

// BenchmarkRS256Sign times one ID token signature (RSA-2048, PKCS #1 v1.5, SHA-256), the floor
// under every Auth call that issues a token: sign-up, sign-in and refresh.
//
//	go test ./tools/bench -run '^$' -bench RS256 -cpu 1,8
func BenchmarkRS256Sign(b *testing.B) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		b.Fatal(err)
	}
	digest := sha256.Sum256([]byte("header.payload"))
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:]); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkRS256Verify times one signature check, what accounts:lookup and the custom token
// sign-in pay on the server, and the verification libraries locally.
func BenchmarkRS256Verify(b *testing.B) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		b.Fatal(err)
	}
	digest := sha256.Sum256([]byte("header.payload"))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
