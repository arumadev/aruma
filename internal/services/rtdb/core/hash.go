package core

import (
	"crypto/sha1"
	"encoding/base64"
	"math"
	"strconv"
	"strings"
)

// Hash is the SDKs' hash of a node: SHA-1, base64 with padding, of a canonical text
// (LeafNode.hash and ChildrenNode.hash in firebase-js-sdk; sha1 in @firebase/util). Null
// hashes to "". It is the h of a conditional put, which the server compares to decide
// datastale, and the ETag of REST.
//
//	leaf:   [priority:<p>:]<type>:<value>     type is boolean, number or string;
//	                                          a number is its IEEE-754 bits in 16 hex digits
//	object: [priority:<p>:]:<key>:<hash>...   children in priority order, null ones skipped
//	<p>:    number:<hex> or string:<text>
func Hash(n *Node) string {
	text := hashText(n)
	if text == "" {
		return ""
	}
	sum := sha1.Sum([]byte(text))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// hashText is the canonical text Hash digests.
func hashText(n *Node) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	if n.priority != nil {
		b.WriteString("priority:")
		b.WriteString(priorityHashText(n.priority))
		b.WriteByte(':')
	}
	switch n.kind {
	case KindBool:
		b.WriteString("boolean:")
		b.WriteString(strconv.FormatBool(n.b))
	case KindNumber:
		b.WriteString("number:")
		b.WriteString(ieee754Hex(n.n))
	case KindString:
		b.WriteString("string:")
		b.WriteString(n.s)
	case KindObject:
		for _, k := range n.SortedKeys(ByPriority) {
			if h := Hash(n.children[k]); h != "" {
				b.WriteByte(':')
				b.WriteString(k)
				b.WriteByte(':')
				b.WriteString(h)
			}
		}
	case kindServerValue:
		// Unresolved placeholders are never hashed; the SDK would hash the object form.
		b.WriteString("object:[object Object]")
	}
	return b.String()
}

// priorityHashText is priorityHashText in snap.ts.
func priorityHashText(p *Node) string {
	if p.Kind() == KindNumber {
		return "number:" + ieee754Hex(p.n)
	}
	return "string:" + p.StringValue()
}

// ieee754Hex is the double's 64 bits, big-endian, in lower-case hex: what the JS SDK's
// doubleToIEEE754String (util.ts:393-445) means to compute. That function gets the bits wrong
// for some numbers, while the emulator hashes the true bits (testdata/sdk/emulator_etags.json),
// as Android does (Utilities.doubleToHashString). The server follows the emulator, so a JS
// transaction on one of these values meets datastale here as it does against the emulator:
//
//   - the largest double below each power of two from 2^2 up (3.9999999999999996, ...);
//   - the integers 2^48-1 through 2^53-1;
//   - 219 negative powers of two between 2^-1021 and 2^-29;
//   - other magnitudes below 2^-971, where Math.pow(2, 52-ln) overflows and the mantissa
//     comes out 0 (testdata/sdk/hash.json, "ieee754").
func ieee754Hex(f float64) string {
	s := strconv.FormatUint(math.Float64bits(f), 16)
	return strings.Repeat("0", 16-len(s)) + s
}
