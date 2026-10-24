package core

import (
	"crypto/md5"
	"encoding"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
)

// An object's checksums, computed while its bytes stream in (ADR-022 §6.2, §6.3): crc32c
// (Castagnoli) and md5, as GCS keeps them. An upload session saves the two hashes' state with its
// received offset (hash_state), through the standard library's encoding.BinaryMarshaler, so a
// finalize days later needs no second read of the staging file.

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// hashStateVersion starts every saved state, so a later format reads as unknown, never as wrong.
const hashStateVersion = 1

// hasher computes both checksums of one object.
type hasher struct {
	crc hash.Hash32
	md5 hash.Hash
}

func newHasher() *hasher { return &hasher{crc: crc32.New(castagnoli), md5: md5.New()} }

func (h *hasher) Write(p []byte) (int, error) {
	h.crc.Write(p)
	h.md5.Write(p)
	return len(p), nil
}

// sums returns the checksums of what was written so far.
func (h *hasher) sums() (crc uint32, sum [16]byte) {
	copy(sum[:], h.md5.Sum(nil))
	return h.crc.Sum32(), sum
}

// marshal returns the hashes' state: a version byte, the crc32 state's length, then both states.
func (h *hasher) marshal() ([]byte, error) {
	c, err := h.crc.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("storage: save the crc32c state: %w", err)
	}
	m, err := h.md5.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return nil, fmt.Errorf("storage: save the md5 state: %w", err)
	}
	out := make([]byte, 0, 3+len(c)+len(m))
	out = append(out, hashStateVersion)
	out = binary.BigEndian.AppendUint16(out, uint16(len(c)))
	out = append(out, c...)
	return append(out, m...), nil
}

// restoreHasher returns the hasher a saved state describes; no state is a hasher at offset 0.
func restoreHasher(state []byte) (*hasher, error) {
	h := newHasher()
	if len(state) == 0 {
		return h, nil
	}
	if len(state) < 3 || state[0] != hashStateVersion {
		return nil, errors.New("storage: an upload session's hash state is not one this version reads")
	}
	n := int(binary.BigEndian.Uint16(state[1:3]))
	if len(state) < 3+n {
		return nil, errors.New("storage: an upload session's hash state is truncated")
	}
	if err := h.crc.(encoding.BinaryUnmarshaler).UnmarshalBinary(state[3 : 3+n]); err != nil {
		return nil, fmt.Errorf("storage: restore the crc32c state: %w", err)
	}
	if err := h.md5.(encoding.BinaryUnmarshaler).UnmarshalBinary(state[3+n:]); err != nil {
		return nil, fmt.Errorf("storage: restore the md5 state: %w", err)
	}
	return h, nil
}

// hashingReader hashes what it reads, and counts it, so a caller can tell whether what a store
// wrote is exactly what was hashed.
type hashingReader struct {
	r io.Reader
	h *hasher
	n int64
}

func (r *hashingReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.h.Write(p[:n])
		r.n += int64(n)
	}
	return n, err
}

// checkChecksums compares what a client claims with what was computed: a claim that does not
// match is ErrChecksumMismatch (ADR-022 §6.1, §6.2).
func checkChecksums(want Checksums, crc uint32, sum [16]byte) error {
	if want.CRC32C != nil && *want.CRC32C != crc {
		return ErrChecksumMismatch
	}
	if want.MD5 != nil && *want.MD5 != sum {
		return ErrChecksumMismatch
	}
	return nil
}

// mergeChecksums joins the claims of an upload's start and of its last request (Go sends
// X-Goog-Hash on the last one alone): two claims of one checksum that differ contradict each
// other, ErrChecksumMismatch.
func mergeChecksums(a, b Checksums) (Checksums, error) {
	out := a
	if b.CRC32C != nil {
		if a.CRC32C != nil && *a.CRC32C != *b.CRC32C {
			return Checksums{}, ErrChecksumMismatch
		}
		out.CRC32C = b.CRC32C
	}
	if b.MD5 != nil {
		if a.MD5 != nil && *a.MD5 != *b.MD5 {
			return Checksums{}, ErrChecksumMismatch
		}
		out.MD5 = b.MD5
	}
	return out, nil
}
