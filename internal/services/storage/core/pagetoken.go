package core

import (
	"crypto/hmac"
	"encoding/base64"
	"encoding/binary"
)

// A list's page token (ADR-022 §13): opaque, and signed with the storage page-token key
// (crypto.Keys.PageTokenMAC). It carries where the page ended, the last name or prefix it
// returned; the signature binds that to the list it came from: the project, the bucket, the
// prefix, the delimiter, the offsets and the surface (the last three: ADR-023 §3-45). A token
// that does not verify against the request it comes with, malformed, forged, or of another list
// or project, is ErrInvalidPageToken (400), never a list from the start, as the emulator answers.

// pageTokenVersion is the token format's first byte.
const pageTokenVersion = 1

// The kinds of a page's last entry.
const (
	cursorObject = 'o'
	cursorPrefix = 'p'
)

// pageCursor is where a page ended.
type pageCursor struct {
	last   string
	prefix bool // last is a prefix, whose names the next page skips
}

// next returns the first name the next page may hold, and false when nothing can follow.
func (c pageCursor) next() (string, bool) {
	if c.prefix {
		return pastPrefix(c.last)
	}
	return after(c.last), true
}

// encodePageToken returns the token of the page that ended at c.
func (s *Service) encodePageToken(b Bucket, o ListOptions, c pageCursor) string {
	kind := byte(cursorObject)
	if c.prefix {
		kind = cursorPrefix
	}
	payload := append([]byte{pageTokenVersion, kind}, c.last...)
	mac := s.keys.PageTokenMAC(pageTokenMessage(b, o, payload))
	return base64.RawURLEncoding.EncodeToString(append(payload, mac...))
}

// decodePageToken verifies o.PageToken against the list o asks for, and returns where its page
// ended.
func (s *Service) decodePageToken(b Bucket, o ListOptions) (pageCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(o.PageToken)
	macSize := len(s.keys.PageTokenMAC(nil))
	if err != nil || len(raw) < 2+macSize || len(raw) > 2+maxNameBytes+macSize {
		return pageCursor{}, ErrInvalidPageToken
	}
	payload, mac := raw[:len(raw)-macSize], raw[len(raw)-macSize:]
	if !hmac.Equal(mac, s.keys.PageTokenMAC(pageTokenMessage(b, o, payload))) || payload[0] != pageTokenVersion {
		return pageCursor{}, ErrInvalidPageToken
	}
	c := pageCursor{last: string(payload[2:])}
	switch payload[1] {
	case cursorObject:
	case cursorPrefix:
		c.prefix = true
	default:
		return pageCursor{}, ErrInvalidPageToken
	}
	return c, nil
}

// pageTokenMessage is what a page token signs: its purpose, the list it belongs to, each part
// prefixed with its length so no two lists give the same bytes, and the payload.
func pageTokenMessage(b Bucket, o ListOptions, payload []byte) []byte {
	msg := []byte("storage.objects.list|")
	msg = binary.BigEndian.AppendUint64(msg, uint64(b.Project))
	msg = append(msg, byte(o.Surface))
	for _, part := range []string{b.Name, o.Prefix, o.Delimiter, o.StartOffset, o.EndOffset, string(payload)} {
		msg = binary.AppendUvarint(msg, uint64(len(part)))
		msg = append(msg, part...)
	}
	return msg
}
