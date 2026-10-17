package crypto

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2Params are argon2id cost parameters.
type Argon2Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
}

// DefaultArgon2 is the OWASP Password Storage Cheat Sheet configuration (m=19 MiB, t=2, p=1).
var DefaultArgon2 = Argon2Params{Memory: 19 * 1024, Time: 2, Threads: 1}

const (
	saltLen = 16
	keyLen  = 32
	// MinPasswordLen and MaxPasswordLen bound accepted passwords. Longer input is rejected,
	// never truncated.
	MinPasswordLen = 8
	MaxPasswordLen = 1024
)

// ErrPasswordLength is returned for passwords outside the accepted length.
var ErrPasswordLength = fmt.Errorf("password must be %d to %d bytes", MinPasswordLen, MaxPasswordLen)

// Hasher hashes and verifies passwords. Each hash allocates Memory KiB, so a semaphore caps how
// many run at once; a burst of logins queues instead of exhausting memory.
type Hasher struct {
	params Argon2Params
	sem    chan struct{}
	dummy  string
}

// NewHasher returns a hasher allowing at most concurrency simultaneous hashes.
func NewHasher(p Argon2Params, concurrency int) *Hasher {
	if concurrency < 1 {
		concurrency = 1
	}
	h := &Hasher{params: p, sem: make(chan struct{}, concurrency)}
	h.dummy = encode(p, RandomBytes(saltLen), RandomBytes(keyLen))
	return h
}

// Hash returns the PHC string for a console password.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	if len(password) < MinPasswordLen {
		return "", ErrPasswordLength
	}
	return h.HashPolicy(ctx, password)
}

// HashPolicy hashes a password whose minimum length the caller's own policy has already
// checked, as Auth's per-project policy does. The upper bound still applies, since Verify
// never accepts longer input.
func (h *Hasher) HashPolicy(ctx context.Context, password string) (string, error) {
	if password == "" || len(password) > MaxPasswordLen {
		return "", ErrPasswordLength
	}
	if err := h.acquire(ctx); err != nil {
		return "", err
	}
	defer h.release()
	salt := RandomBytes(saltLen)
	key := argon2.IDKey([]byte(password), salt, h.params.Time, h.params.Memory, h.params.Threads, keyLen)
	return encode(h.params, salt, key), nil
}

// Verify checks password against a PHC string. needsRehash is true when the stored hash uses
// other parameters than the current ones.
func (h *Hasher) Verify(ctx context.Context, password, phc string) (ok, needsRehash bool, err error) {
	p, salt, want, err := decode(phc)
	if err != nil {
		return false, false, err
	}
	if len(password) > MaxPasswordLen {
		// Still spend the time so the length is not a timing oracle.
		password = password[:MaxPasswordLen]
		defer func() { ok = false }()
	}
	if err := h.acquire(ctx); err != nil {
		return false, false, err
	}
	defer h.release()
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want)))
	ok = subtle.ConstantTimeCompare(got, want) == 1
	return ok, p != h.params, nil
}

// VerifyDummy spends the same time as a real verification. Login calls it for unknown or
// disabled accounts so timing does not reveal which accounts exist.
func (h *Hasher) VerifyDummy(ctx context.Context, password string) {
	_, _, _ = h.Verify(ctx, password, h.dummy)
}

func (h *Hasher) acquire(ctx context.Context) error {
	select {
	case h.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hasher) release() { <-h.sem }

func encode(p Argon2Params, salt, key []byte) string {
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key))
}

var errBadHash = errors.New("crypto: malformed password hash")

func decode(phc string) (Argon2Params, []byte, []byte, error) {
	var p Argon2Params
	parts := strings.Split(phc, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, errBadHash
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return p, nil, nil, errBadHash
	}
	var threads uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &threads); err != nil ||
		p.Memory == 0 || p.Time == 0 || threads == 0 || threads > 255 {
		return p, nil, nil, errBadHash
	}
	p.Threads = uint8(threads)
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return p, nil, nil, errBadHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) < 16 {
		return p, nil, nil, errBadHash
	}
	return p, salt, key, nil
}
