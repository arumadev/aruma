package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strconv"
	"strings"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

// Path is a path below a database's documents/: collection ids at even indexes, document ids at
// odd ones. A document's path has an even length, a collection's an odd one; the root is empty.
type Path []string

// ParsePath splits a path written with slashes; "" is the root. The transport has checked it.
func ParsePath(s string) Path {
	if s == "" {
		return nil
	}
	return strings.Split(s, "/")
}

func (p Path) String() string { return strings.Join(p, "/") }

// IsDocument reports whether p names a document.
func (p Path) IsDocument() bool { return len(p) > 0 && len(p)%2 == 0 }

// Parent returns the document a document's collection is under: nil for a top-level collection.
func (p Path) Parent() Path { return p[: len(p)-2 : len(p)-2] }

// CollectionID returns the id of a document's collection.
func (p Path) CollectionID() string { return p[len(p)-2] }

// ID returns the last segment: a document's or a collection's id.
func (p Path) ID() string { return p[len(p)-1] }

// Child returns the path of the document id in the collection collectionID under p.
func (p Path) Child(collectionID, id string) Path {
	return append(p[:len(p):len(p)], collectionID, id)
}

// Name keys (ADR-024 §1.3). A document's key is value.KeyVersion, each segment as a reference's
// sort key writes it (value.Key: 0x01 and 8 sign-flipped bytes for a numeric id __id<n>__, which
// sorts first; else 0x02, the bytes with 0x00 and 0xFF escaped, and 0x00 0x01), then 0x00. So
// bytes.Compare of two keys is the order of the names, a document comes before its
// descendants, and no key is a prefix of another. A prefix key (the segments without the final
// 0x00) begins the keys of everything below a document or a collection.

const (
	segNumeric = 0x01
	segText    = 0x02
	keyEnd     = 0x00
	// pastChildren follows the segment bytes of every child: a child's keys continue a prefix
	// with 0x00, 0x01 or 0x02.
	pastChildren = 0x03

	// cutLen is where a stored key is cut, and hashLen the hash that follows (ADR-024 §1.4).
	cutLen  = 2048
	hashLen = sha256.Size
)

// appendSegment appends one path segment's key.
func appendSegment(dst []byte, seg string) []byte {
	if n, ok := numericID(seg); ok {
		dst = append(dst, segNumeric)
		return binary.BigEndian.AppendUint64(dst, uint64(n)^1<<63)
	}
	dst = append(dst, segText)
	for i := range len(seg) {
		switch c := seg[i]; c {
		case 0x00:
			dst = append(dst, 0x00, 0xFF)
		case 0xFF:
			dst = append(dst, 0xFF, 0x00)
		default:
			dst = append(dst, c)
		}
	}
	return append(dst, 0x00, 0x01)
}

// numericID parses a numeric id, __id<n>__ with n in canonical decimal, as value's comparison
// does: such ids sort before every other, by n.
func numericID(s string) (int64, bool) {
	digits, ok := strings.CutPrefix(s, "__id")
	if !ok {
		return 0, false
	}
	if digits, ok = strings.CutSuffix(digits, "__"); !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != digits {
		return 0, false
	}
	return n, true
}

// prefixKey returns the key prefix of everything below p.
func prefixKey(p Path) []byte {
	k := []byte{value.KeyVersion}
	for _, seg := range p {
		k = appendSegment(k, seg)
	}
	return k
}

// nameKey returns a document's whole name key.
func nameKey(p Path) []byte { return append(prefixKey(p), keyEnd) }

// storedKey returns the form a key column holds: the key itself up to cutLen bytes, else its
// first cutLen bytes and the SHA-256 of all of it.
func storedKey(k []byte) []byte {
	if len(k) <= cutLen {
		return k
	}
	sum := sha256.Sum256(k)
	return append(bytes.Clone(k[:cutLen]), sum[:]...)
}

// lowerBound returns the stored form of an inclusive lower bound on whole keys: every key at or
// above k is stored at or above it.
func lowerBound(k []byte) []byte {
	if len(k) <= cutLen {
		return k
	}
	return k[:cutLen:cutLen]
}

// upperBound returns the stored form of an exclusive upper bound on whole keys: every key below
// k is stored below it. A cut bound passes every key that shares its first cutLen bytes, some of
// them past k, which the caller drops by the whole key.
func upperBound(k []byte) []byte {
	if len(k) <= cutLen {
		return k
	}
	return append(bytes.Clone(k[:cutLen]), bytes.Repeat([]byte{0xFF}, hashLen+1)...)
}

// chainBounds returns the stored keys of the whole keys that share their first cutLen bytes with
// k, a key longer than that: core orders such a run by the whole key (ADR-024 §1.4).
func chainBounds(k []byte) (lo, hi []byte) {
	return k[:cutLen:cutLen], append(bytes.Clone(k[:cutLen]), bytes.Repeat([]byte{0xFF}, hashLen)...)
}

// childEnd returns the end of the segment that starts at byte plen of key k: the length of the
// prefix of the child of k[:plen] that k is in or below.
func childEnd(k []byte, plen int) (int, error) {
	if plen >= len(k) {
		return 0, errBadKey
	}
	switch k[plen] {
	case segNumeric:
		if plen+9 > len(k) {
			return 0, errBadKey
		}
		return plen + 9, nil
	case segText:
		i := bytes.Index(k[plen+1:], []byte{0x00, 0x01})
		if i < 0 {
			return 0, errBadKey
		}
		return plen + 1 + i + 2, nil
	}
	return 0, errBadKey
}

var errBadKey = errors.New("documents: malformed name key")

// segmentAt decodes the segment at byte plen of k, and returns it and its end.
func segmentAt(k []byte, plen int) (string, int, error) {
	end, err := childEnd(k, plen)
	if err != nil {
		return "", 0, err
	}
	if k[plen] == segNumeric {
		n := int64(binary.BigEndian.Uint64(k[plen+1:end]) ^ 1<<63)
		return "__id" + strconv.FormatInt(n, 10) + "__", end, nil
	}
	b := make([]byte, 0, end-plen-3)
	for i := plen + 1; i < end-2; i++ {
		c := k[i]
		if (c == 0x00 || c == 0xFF) && i+1 < end-2 {
			i++ // the escape's second byte
		}
		b = append(b, c)
	}
	return string(b), end, nil
}

// pathOfKey decodes a document's whole name key.
func pathOfKey(k []byte) (Path, error) {
	if len(k) < 2 || k[0] != value.KeyVersion || k[len(k)-1] != keyEnd {
		return nil, errBadKey
	}
	var p Path
	for i := 1; i < len(k)-1; {
		seg, end, err := segmentAt(k, i)
		if err != nil {
			return nil, err
		}
		p = append(p, seg)
		i = end
	}
	if !p.IsDocument() {
		return nil, errBadKey
	}
	return p, nil
}

// docKeys is what a documents row holds of its document's name.
type docKeys struct {
	name   []byte // the whole name key
	stored []byte // name_key
	// parent is the parent document's name key, empty for a top-level collection; parentHash
	// the SHA-256 of the collection's prefix key; cgHash the SHA-256 of the collection id.
	parent     []byte
	parentHash []byte
	cgHash     []byte
}

func keysOf(p Path) docKeys {
	k := docKeys{name: nameKey(p)}
	k.stored = storedKey(k.name)
	if parent := p.Parent(); len(parent) > 0 {
		k.parent = nameKey(parent)
	} else {
		k.parent = []byte{}
	}
	ph := sha256.Sum256(collectionPrefix(p.Parent(), p.CollectionID()))
	cg := sha256.Sum256([]byte(p.CollectionID()))
	k.parentHash, k.cgHash = ph[:], cg[:]
	return k
}

// collectionPrefix returns the prefix key of the collection id under parent.
func collectionPrefix(parent Path, id string) []byte {
	return appendSegment(prefixKey(parent), id)
}
