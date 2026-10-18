package tokens

import "crypto/rsa"

// Tokens signed by others: the ID tokens Google and Apple issue. They go through the same
// strict codec as Aruma's own, so only RS256 with a kid is ever considered.

// KidOf returns the key id of an RS256 JWT, or ErrMalformed.
func KidOf(token string) (string, error) {
	p, err := parse(token)
	if err != nil {
		return "", err
	}
	return p.header.Kid, nil
}

// VerifyWith checks an RS256 JWT against pub and returns its payload. The caller checks the
// claims.
func VerifyWith(token string, pub *rsa.PublicKey) ([]byte, error) {
	p, err := parse(token)
	if err != nil {
		return nil, err
	}
	if pub == nil || !p.verify(pub) {
		return nil, ErrSignature
	}
	return p.payload, nil
}

// PublicKey decodes an RSA JWK.
func (j JWK) PublicKey() (*rsa.PublicKey, error) { return j.publicKey() }
