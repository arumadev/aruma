package tokens

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
)

// A minimal RS256 JWT codec. It accepts nothing but RS256 with a kid, so "alg: none", HMAC
// confusion and unsigned tokens are rejected before any key is looked at (ADR-004 §6).

var (
	// ErrMalformed covers anything that is not a well-formed RS256 JWT with a kid.
	ErrMalformed = errors.New("tokens: malformed token")
	// ErrSignature means no published key verifies the token.
	ErrSignature = errors.New("tokens: invalid signature")
)

// Header is the JOSE header Aruma writes and accepts.
type Header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ,omitempty"`
}

func encodeSegment(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// sign builds header.payload.signature.
func sign(kid string, key *rsa.PrivateKey, claims any) (string, error) {
	h, err := encodeSegment(Header{Alg: "RS256", Kid: kid, Typ: "JWT"})
	if err != nil {
		return "", err
	}
	p, err := encodeSegment(claims)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(h + "." + p))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return h + "." + p + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parsed is a token split and decoded, not yet verified.
type parsed struct {
	header    Header
	payload   []byte
	signing   string
	signature []byte
}

func parse(token string) (*parsed, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 16<<10 {
		return nil, ErrMalformed
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrMalformed
	}
	var h Header
	if err := json.Unmarshal(hb, &h); err != nil || h.Alg != "RS256" || h.Kid == "" {
		return nil, ErrMalformed
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !json.Valid(payload) {
		return nil, ErrMalformed
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) == 0 {
		return nil, ErrMalformed
	}
	return &parsed{header: h, payload: payload, signing: parts[0] + "." + parts[1], signature: sig}, nil
}

func (p *parsed) verify(pub *rsa.PublicKey) bool {
	digest := sha256.Sum256([]byte(p.signing))
	return rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], p.signature) == nil
}

// JWK is a public RSA key as published in a JWKS.
type JWK struct {
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func toJWK(kid string, pub *rsa.PublicKey) JWK {
	e := []byte{byte(pub.E >> 16), byte(pub.E >> 8), byte(pub.E)}
	for len(e) > 1 && e[0] == 0 {
		e = e[1:]
	}
	return JWK{Kty: "RSA", Alg: "RS256", Use: "sig", Kid: kid,
		N: base64.RawURLEncoding.EncodeToString(pub.N.Bytes()), E: base64.RawURLEncoding.EncodeToString(e)}
}

func (j JWK) publicKey() (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(j.N)
	if err != nil {
		return nil, err
	}
	e, err := base64.RawURLEncoding.DecodeString(j.E)
	if err != nil {
		return nil, err
	}
	var ei int
	for _, b := range e {
		ei = ei<<8 | int(b)
	}
	pub := &rsa.PublicKey{E: ei}
	pub.N = new(big.Int).SetBytes(n)
	return pub, nil
}
