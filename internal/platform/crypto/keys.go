// Package crypto holds Aruma's cryptographic primitives: key derivation from the master key,
// random tokens stored only as HMACs, argon2id password hashes and envelope encryption.
//
// Every purpose gets its own key derived from the master key with HKDF, so a key used for
// one job can never be confused with another.
package crypto

import (
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"
)

// Keys are the per-purpose keys derived from the master key.
type Keys struct {
	tokenMAC     []byte
	csrfMAC      []byte
	pageTokenMAC []byte
	kek          map[uint32][]byte
	current      uint32
}

// NewKeys derives every purpose key from a 32-byte master key. The key-encryption key is
// version 1; later versions come from rotated master keys (see AddKEK).
func NewKeys(master []byte) (*Keys, error) {
	if len(master) != 32 {
		return nil, errors.New("crypto: master key must be 32 bytes")
	}
	mac, err := derive(master, "aruma/v1/token-hmac")
	if err != nil {
		return nil, err
	}
	csrf, err := derive(master, "aruma/v1/csrf")
	if err != nil {
		return nil, err
	}
	page, err := derive(master, "aruma/v1/storage-page-token")
	if err != nil {
		return nil, err
	}
	kek, err := derive(master, "aruma/v1/kek")
	if err != nil {
		return nil, err
	}
	return &Keys{tokenMAC: mac, csrfMAC: csrf, pageTokenMAC: page, kek: map[uint32][]byte{1: kek}, current: 1}, nil
}

// AddKEK registers the key-encryption key derived from a newer master key under version v and
// makes it current. Older versions stay available to open existing data.
func (k *Keys) AddKEK(v uint32, master []byte) error {
	if v <= k.current {
		return fmt.Errorf("crypto: KEK version %d must be above %d", v, k.current)
	}
	if len(master) != 32 {
		return errors.New("crypto: master key must be 32 bytes")
	}
	kek, err := derive(master, "aruma/v1/kek")
	if err != nil {
		return err
	}
	k.kek[v] = kek
	k.current = v
	return nil
}

func derive(master []byte, info string) ([]byte, error) {
	return hkdf.Key(sha256.New, master, nil, info, 32)
}
