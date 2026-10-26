package value

import "strings"

// The size limits, as production enforces them.
const (
	// MaxDocumentSize is the largest document, by DocumentSize: "cannot be written because its
	// size (1,048,577 bytes) exceeds the maximum allowed size of 1,048,576 bytes" (f0-errors.json).
	MaxDocumentSize = 1 << 20
	// MaxStringBytes is the longest string or bytes value, in bytes, at any depth (values-size.json,
	// cap.*): 1 MiB less 89. Maps and arrays have no cap of their own beyond the document's.
	MaxStringBytes = MaxDocumentSize - 89
)

// Size returns a value's size by Google's storage-size formula
// (firebase.google.com/docs/firestore/storage-size), as production computes it: production's
// "exceeds the maximum allowed size" message gives the same number for every type
// (values-size.json). A string is its UTF-8 length plus 1, bytes their length, null and
// booleans 1, numbers and timestamps 8, a geopoint 16, a reference its document name's size
// (NameSize), an array the sum of its elements, a map the sum of its field names plus 1 and
// their values, and a vector 8 per dimension (not the map it travels as). An empty array or
// map is 1, which the docs do not say. A BSON shape counts as its type, not as its map
// (values-bson.json, size.*): MinKey and MaxKey 1, int32 4, Decimal128 16, a BSON timestamp 8,
// binary its bytes (the subtype byte included), an ObjectId 12, a regex its pattern and its
// options, each as a string.
func Size(v Value) int {
	switch v.kind {
	case KindNull, KindBoolean:
		return 1
	case KindInteger, KindDouble, KindTimestamp:
		return 8
	case KindGeoPoint:
		return 16
	case KindString:
		return len(v.s) + 1
	case KindBytes:
		return len(v.s)
	case KindReference:
		return NameSize(v.s)
	case KindArray:
		if len(v.list) == 0 {
			return 1
		}
		n := 0
		for _, e := range v.list {
			n += Size(e)
		}
		return n
	}
	if v.IsVector() {
		return 8 * len(v.vectorElements())
	}
	if b, ok := bsonOf(v); ok {
		return bsonSize(b)
	}
	if len(v.m) == 0 {
		return 1
	}
	return fieldsSize(v.m)
}

func bsonSize(b bson) int {
	switch b.kind {
	case BSONMinKey, BSONMaxKey:
		return 1
	case BSONInt32:
		return 4
	case BSONDecimal128:
		return 16
	case BSONTimestamp:
		return 8
	case BSONObjectID:
		return 12
	case BSONRegex:
		return len(b.s) + 1 + len(b.o) + 1
	}
	return len(b.s) // binary
}

// IndexSize returns the size of v an index entry would hold uncut: Size, except that an empty
// array or map counts nothing ([x×1498, [], false] and [..., true] sort apart,
// values-order.json tr-empty-*). See IndexedWhole for what production refuses as too large.
func IndexSize(v Value) int {
	switch v.kind {
	case KindArray:
		n := 0
		for _, e := range v.list {
			n += IndexSize(e)
		}
		return n
	case KindMap:
		if v.IsVector() || v.BSONKind() != NotBSON {
			return Size(v)
		}
		n := 0
		for _, f := range v.m {
			n += len(f.Name) + 1 + IndexSize(f.Value)
		}
		return n
	}
	return Size(v)
}

func fieldsSize(fields []Field) int {
	n := 0
	for _, f := range fields {
		n += len(f.Name) + 1 + Size(f.Value)
	}
	return n
}

// NameSize returns a document name's size: each collection and document id's UTF-8 length plus
// 1, plus 16. Only the path below documents/ counts, so a reference to another database or
// project costs the same as one to this. name is a full resource name or that path.
func NameSize(name string) int {
	if _, path, ok := strings.Cut(name, "/documents/"); ok && strings.HasPrefix(name, "projects/") {
		name = path
	}
	n := 16
	for seg := range strings.SplitSeq(name, "/") {
		n += len(seg) + 1
	}
	return n
}

// DocumentSize returns the size of a document of that name and fields (a map value): the
// name's size, each field's name plus 1 and its value, and 32. Production measures its 1 MiB
// limit this way, to the byte (f0-errors.json, doc_size.*).
func DocumentSize(name string, fields Value) int {
	return NameSize(name) + fieldsSize(fields.Fields()) + 32
}
