package crypto

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// TokenBytes is the entropy of every bearer token: 256 bits.
const TokenBytes = 32

// NewToken returns a fresh random token, prefixed so it is recognisable (and scannable) in
// leaked text, e.g. "aruma_pat_...". The plaintext is shown once and never stored.
func NewToken(prefix string) string {
	return prefix + RandomString(TokenBytes)
}

// RandomString returns n random bytes, base64url-encoded without padding.
func RandomString(n int) string {
	return base64.RawURLEncoding.EncodeToString(RandomBytes(n))
}

// RandomBytes returns n bytes from the operating system's CSPRNG.
func RandomBytes(n int) []byte {
	b := make([]byte, n)
	// crypto/rand.Read never fails on supported platforms; it panics internally if it would.
	_, _ = rand.Read(b)
	return b
}

// RandomHex returns n random bytes, hex-encoded.
func RandomHex(n int) string { return hex.EncodeToString(RandomBytes(n)) }

// HashToken returns the HMAC-SHA256 of a token, the only form tokens are stored in. A database
// dump alone therefore yields no usable token.
func (k *Keys) HashToken(token string) []byte {
	m := hmac.New(sha256.New, k.tokenMAC)
	m.Write([]byte(token))
	return m.Sum(nil)
}

// EqualHash compares two token hashes in constant time.
func EqualHash(a, b []byte) bool { return hmac.Equal(a, b) }

// CSRFToken derives the CSRF token bound to a session token. Nothing is stored: the server
// recomputes it from the session cookie, and another session's token never matches.
func (k *Keys) CSRFToken(sessionToken string) string {
	m := hmac.New(sha256.New, k.csrfMAC)
	m.Write([]byte(sessionToken))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// CheckCSRF reports, in constant time, whether token is the CSRF token of sessionToken.
func (k *Keys) CheckCSRF(sessionToken, token string) bool {
	return hmac.Equal([]byte(k.CSRFToken(sessionToken)), []byte(token))
}

// PageTokenMAC returns the HMAC-SHA256 of a list page token's contents, under a key of its own
// so a page token can never pass for any other token (ADR-022 §13). Storage signs with it what a
// page continues: the project, the bucket, the prefix, the delimiter and the last name returned.
func (k *Keys) PageTokenMAC(msg []byte) []byte {
	m := hmac.New(sha256.New, k.pageTokenMAC)
	m.Write(msg)
	return m.Sum(nil)
}
