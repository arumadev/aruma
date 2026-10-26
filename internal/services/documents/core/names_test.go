package core

import (
	"bytes"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

// randomPath returns a document path of 1 to 3 levels whose segments mix numeric ids, plain ids
// and ids holding 0x00, 0xFF and shared prefixes, so that the order is exercised where it is
// delicate.
func randomPath(r *rand.Rand) Path {
	pieces := []string{"a", "ab", "b", "__id1__", "__id-5__", "__id12__", "__id01__", "a\x00", "a\xff", "\xff", "z", "é"}
	var p Path
	for range 2 * (1 + r.IntN(3)) {
		seg := pieces[r.IntN(len(pieces))]
		if r.IntN(4) == 0 {
			seg += pieces[r.IntN(len(pieces))]
		}
		p = append(p, seg)
	}
	return p
}

// The name key orders documents as production orders __name__, which value.Compare decides on
// references: the key must agree with it on every pair (ADR-024 §1.3).
func TestNameKeyOrderIsReferenceOrder(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	db := DB{ProjectID: "chat-app", ID: DefaultDatabase}
	for range 20000 {
		a, b := randomPath(r), randomPath(r)
		want := value.Compare(value.Reference(db.DocumentName(a)), value.Reference(db.DocumentName(b)))
		if got := bytes.Compare(nameKey(a), nameKey(b)); sign(got) != sign(want) {
			t.Fatalf("%q vs %q: key order %d, value order %d", a, b, got, want)
		}
		if p, err := pathOfKey(nameKey(a)); err != nil || !slices.Equal(p, a) {
			t.Fatalf("pathOfKey(nameKey(%q)) = %q, %v", a, p, err)
		}
	}
}

func sign(n int) int { return min(max(n, -1), 1) }

// A document's key comes before its descendants', and the keys below a document or a collection
// lie between the prefix and the prefix followed by pastChildren.
func TestNameKeyRanges(t *testing.T) {
	doc := Path{"c", "d"}
	below := []Path{{"c", "d", "e", "f"}, {"c", "d", "__id3__", "x"}, {"c", "d", "\xff", "x", "y", "z"}}
	for _, b := range below {
		k := nameKey(b)
		if bytes.Compare(nameKey(doc), k) >= 0 {
			t.Errorf("%q does not sort after its ancestor", b)
		}
		if !bytes.HasPrefix(k, prefixKey(doc)) || bytes.Compare(k, append(prefixKey(doc), pastChildren)) >= 0 {
			t.Errorf("%q is not in its ancestor's range", b)
		}
		if end, err := childEnd(k, len(prefixKey(doc))); err != nil || !bytes.Equal(k[:end], collectionPrefix(doc, b[2])) {
			t.Errorf("childEnd(%q) = %d, %v", b, end, err)
		}
	}
	if !inCollection(nameKey(Path{"c", "d"}), collectionPrefix(nil, "c")) || inCollection(nameKey(Path{"c", "d", "e", "f"}), collectionPrefix(nil, "c")) {
		t.Error("inCollection")
	}
}

// The stored key is the key up to 2,048 bytes, else 2,048 bytes and the SHA-256: at most 2,080
// bytes, distinct for distinct keys, and in order wherever two keys differ within the cut.
func TestStoredKeyCut(t *testing.T) {
	long := strings.Repeat("x", 1500)
	a := Path{"c", long, "s", long + "a"}
	b := Path{"c", long, "s", long + "b"}
	ka, kb := nameKey(a), nameKey(b)
	if len(ka) <= cutLen {
		t.Fatalf("key of %d bytes is not cut", len(ka))
	}
	sa, sb := storedKey(ka), storedKey(kb)
	if len(sa) != cutLen+hashLen || bytes.Equal(sa, sb) || !bytes.Equal(sa[:cutLen], sb[:cutLen]) {
		t.Fatalf("stored keys: %d bytes, equal %v", len(sa), bytes.Equal(sa, sb))
	}
	short := nameKey(Path{"c", "d"})
	if !bytes.Equal(storedKey(short), short) {
		t.Error("a short key is stored as is")
	}
	lo, hi := chainBounds(ka)
	for _, s := range [][]byte{sa, sb} {
		if bytes.Compare(s, lo) < 0 || bytes.Compare(s, hi) > 0 {
			t.Error("a cut key is outside its run's bounds")
		}
	}
	if bytes.Compare(lowerBound(ka), sa) > 0 || bytes.Compare(upperBound(ka), sb) <= 0 {
		t.Error("cut bounds do not contain the run")
	}
	// A stored key of a document that differs within the cut orders as the whole keys do.
	c := Path{"c", strings.Repeat("w", 1500), "s", "t"}
	if bytes.Compare(storedKey(nameKey(c)), sa) >= 0 || bytes.Compare(nameKey(c), ka) >= 0 {
		t.Error("order within the cut")
	}
}

func TestSegmentAtNumeric(t *testing.T) {
	for _, id := range []string{"__id0__", "__id-9223372036854775808__", "__id9223372036854775807__", "__id01__", "a\x00\xffb"} {
		k := nameKey(Path{"c", id})
		seg, _, err := segmentAt(k, len(collectionPrefix(nil, "c")))
		if err != nil || seg != id {
			t.Errorf("segmentAt of %q = %q, %v", id, seg, err)
		}
	}
}
