package main

import (
	"context"
	"testing"

	"github.com/osama300/aurma-project/internal/platform/crypto"
)

// BenchmarkArgon2Verify times one password check at the production cost, the floor under the
// console-login scenario:
//
//	go test ./tools/bench -run '^$' -bench Argon2 -benchtime 50x
func BenchmarkArgon2Verify(b *testing.B) {
	h := crypto.NewHasher(crypto.DefaultArgon2, 4)
	ctx := context.Background()
	phc, err := h.Hash(ctx, "correct horse battery staple")
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for range b.N {
		if ok, _, err := h.Verify(ctx, "correct horse battery staple", phc); !ok || err != nil {
			b.Fatal("verify failed", err)
		}
	}
}

// BenchmarkArgon2VerifyParallel runs checks from GOMAXPROCS goroutines through the server's
// gate of 4, which gives the login throughput ceiling of this machine (-cpu 4 for four).
func BenchmarkArgon2VerifyParallel(b *testing.B) {
	h := crypto.NewHasher(crypto.DefaultArgon2, 4)
	ctx := context.Background()
	phc, err := h.Hash(ctx, "correct horse battery staple")
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if ok, _, err := h.Verify(ctx, "correct horse battery staple", phc); !ok || err != nil {
				b.Error("verify failed", err)
				return
			}
		}
	})
}
