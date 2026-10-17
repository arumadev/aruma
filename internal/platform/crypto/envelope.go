package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
)

// Envelope format, version 1:
//
//	byte    format version (1)
//	uint32  KEK version, big-endian
//	uint16  length of the wrapped data key
//	[]byte  data key sealed by the KEK  (random nonce prepended)
//	[]byte  plaintext sealed by the data key (random nonce prepended)
//
// Every record gets a fresh 256-bit data key, so no key comes near GCM's per-key message
// limit. Both seals bind the same associated data, which callers set to where the value lives
// (e.g. "control.smtp|password|<row id>"), so a ciphertext copied to another row fails to open.
const envelopeV1 = 1

var errEnvelope = errors.New("crypto: cannot open envelope")

// Seal encrypts plaintext under a fresh data key wrapped by the current KEK.
func (k *Keys) Seal(plaintext, aad []byte) ([]byte, error) {
	dek := RandomBytes(32)
	ad := envelopeAD(aad, k.current)
	wrapped, err := seal(k.kek[k.current], dek, ad)
	if err != nil {
		return nil, err
	}
	body, err := seal(dek, plaintext, ad)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, 1+4+2+len(wrapped)+len(body))
	out = append(out, envelopeV1)
	out = binary.BigEndian.AppendUint32(out, k.current)
	out = binary.BigEndian.AppendUint16(out, uint16(len(wrapped)))
	out = append(out, wrapped...)
	return append(out, body...), nil
}

// Open decrypts an envelope produced by Seal with the same associated data.
func (k *Keys) Open(envelope, aad []byte) ([]byte, error) {
	if len(envelope) < 7 || envelope[0] != envelopeV1 {
		return nil, errEnvelope
	}
	v := binary.BigEndian.Uint32(envelope[1:5])
	n := int(binary.BigEndian.Uint16(envelope[5:7]))
	if len(envelope) < 7+n {
		return nil, errEnvelope
	}
	kek, ok := k.kek[v]
	if !ok {
		return nil, fmt.Errorf("crypto: unknown KEK version %d", v)
	}
	ad := envelopeAD(aad, v)
	dek, err := open(kek, envelope[7:7+n], ad)
	if err != nil {
		return nil, errEnvelope
	}
	pt, err := open(dek, envelope[7+n:], ad)
	if err != nil {
		return nil, errEnvelope
	}
	return pt, nil
}

// NeedsRewrap reports whether an envelope was sealed under an older KEK.
func (k *Keys) NeedsRewrap(envelope []byte) bool {
	return len(envelope) >= 5 && binary.BigEndian.Uint32(envelope[1:5]) != k.current
}

func envelopeAD(aad []byte, kekVersion uint32) []byte {
	ad := append([]byte("aruma:v1|"), aad...)
	return binary.BigEndian.AppendUint32(append(ad, '|'), kekVersion)
}

func gcm(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

func seal(key, plaintext, ad []byte) ([]byte, error) {
	a, err := gcm(key)
	if err != nil {
		return nil, err
	}
	return a.Seal(nil, nil, plaintext, ad), nil
}

func open(key, ciphertext, ad []byte) ([]byte, error) {
	a, err := gcm(key)
	if err != nil {
		return nil, err
	}
	return a.Open(nil, nil, ciphertext, ad)
}
