// Package value is the Document Database's value model: the eleven Firestore value types and
// the maps that stand for vectors and Enterprise BSON types (BSONKind), their total order
// (Compare) and its order-preserving byte encoding (Key), filter equality (Equal) and range
// classes (InRange, ClassBounds), field paths and masks, Google's storage-size formula, and the
// limits a write is held to. It knows nothing of protobuf or JSON: the wire package converts,
// and core stores.
//
// Every rule here is production's (Cloud Firestore, Standard edition), recorded under
// test/oracle/documents/corpus/values-*.json by production/record-values.mjs; where Google's
// docs or the SDKs say otherwise the comment says so.
package value

import (
	"encoding/base64"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Kind is the type of a value. Vectors have no kind of their own: on the wire and here a
// vector is a map {__type__: "__vector__", value: [doubles]} (IsVector), which sorts between
// arrays and maps.
type Kind uint8

const (
	KindNull Kind = iota
	KindBoolean
	KindInteger
	KindDouble
	KindTimestamp
	KindString
	KindBytes
	KindReference
	KindGeoPoint
	KindArray
	KindMap
)

var kindNames = [...]string{"null", "boolean", "integer", "double", "timestamp", "string", "bytes",
	"reference", "geopoint", "array", "map"}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "Kind(" + strconv.Itoa(int(k)) + ")"
}

// Value is one Firestore value. The zero Value is null. Values are immutable once built; the
// constructors copy what they are given.
//
// Server-only sentinels (serverTimestamp, increment, arrayUnion, delete) are not values: they
// travel as field transforms and never reach this package.
type Value struct {
	kind Kind
	i    int64   // Integer; Timestamp seconds; Boolean 0 or 1
	n    int32   // Timestamp nanoseconds
	f    float64 // Double; GeoPoint latitude
	g    float64 // GeoPoint longitude
	s    string  // String; Bytes; Reference (the full resource name)
	list []Value // Array
	m    []Field // Map, sorted by Name (UTF-8 byte order), names unique
}

// Field is one entry of a map value.
type Field struct {
	Name  string
	Value Value
}

// Null returns the null value.
func Null() Value { return Value{} }

// Bool returns a boolean value.
func Bool(b bool) Value {
	v := Value{kind: KindBoolean}
	if b {
		v.i = 1
	}
	return v
}

// Int returns a 64-bit integer value.
func Int(i int64) Value { return Value{kind: KindInteger, i: i} }

// Double returns a double value. NaN and -0.0 are kept as given: production stores and returns
// -0.0 with its sign (values-order.json, n-d-neg-zero), and orders it equal to 0.
func Double(f float64) Value { return Value{kind: KindDouble, f: f} }

// Timestamp returns a timestamp of seconds since the Unix epoch and nanoseconds (0 to
// 999,999,999). Production keeps microseconds and drops the rest on write (values-order.json,
// t-nanos); TruncateMicros does that.
func Timestamp(seconds int64, nanos int32) Value {
	return Value{kind: KindTimestamp, i: seconds, n: nanos}
}

// String returns a string value; s should be valid UTF-8, which the wire guarantees.
func String(s string) Value { return Value{kind: KindString, s: s} }

// Bytes returns a bytes value holding a copy of b.
func Bytes(b []byte) Value { return Value{kind: KindBytes, s: string(b)} }

// Reference returns a reference to a document, by its full resource name
// (projects/{p}/databases/{d}/documents/{path}). The name is not checked here.
func Reference(name string) Value { return Value{kind: KindReference, s: name} }

// GeoPoint returns a geographical point.
func GeoPoint(lat, lng float64) Value { return Value{kind: KindGeoPoint, f: lat, g: lng} }

// Array returns an array of the given values.
func Array(vs ...Value) Value { return Value{kind: KindArray, list: slices.Clone(vs)} }

// Map returns a map of the given fields. A name given twice keeps its last value.
func Map(fields ...Field) Value {
	m := slices.Clone(fields)
	slices.SortStableFunc(m, func(a, b Field) int { return strings.Compare(a.Name, b.Name) })
	// Keep the last of equal names: compact from the end.
	out := m[:0]
	for i, f := range m {
		if i+1 < len(m) && m[i+1].Name == f.Name {
			continue
		}
		out = append(out, f)
	}
	return Value{kind: KindMap, m: slices.Clip(out)}
}

// MapOf returns a map of the entries of fields.
func MapOf(fields map[string]Value) Value {
	m := make([]Field, 0, len(fields))
	for k, v := range fields {
		m = append(m, Field{k, v})
	}
	slices.SortFunc(m, func(a, b Field) int { return strings.Compare(a.Name, b.Name) })
	return Value{kind: KindMap, m: m}
}

// The two keys of a vector's map, and the type marker.
const (
	vectorTypeKey   = "__type__"
	vectorTypeValue = "__vector__"
	vectorValueKey  = "value"
)

// Vector returns a vector value: the map {__type__: "__vector__", value: [doubles]} the SDKs
// send (firestore-clients.md) and production stores.
func Vector(ds ...float64) Value {
	els := make([]Value, len(ds))
	for i, d := range ds {
		els[i] = Double(d)
	}
	return Value{kind: KindMap, m: []Field{
		{vectorTypeKey, String(vectorTypeValue)},
		{vectorValueKey, Value{kind: KindArray, list: els}},
	}}
}

// Kind returns the value's type.
func (v Value) Kind() Kind { return v.kind }

// IsNull reports whether v is null.
func (v Value) IsNull() bool { return v.kind == KindNull }

// IsNaN reports whether v is NaN: the double NaN, or a Decimal128 NaN, which production
// treats alike (IS_NAN matches both; values-bson.json).
func (v Value) IsNaN() bool {
	switch v.kind {
	case KindDouble:
		return math.IsNaN(v.f)
	case KindMap:
		b, ok := bsonOf(v)
		return ok && b.kind == BSONDecimal128 && b.dec.form == decNaN
	}
	return false
}

// IsNumber reports whether v is an integer or a double. An int32 or a Decimal128 is a map
// (BSONKind); Compare orders all four as numbers.
func (v Value) IsNumber() bool { return v.kind == KindInteger || v.kind == KindDouble }

// BoolValue returns a boolean's value, false for any other kind.
func (v Value) BoolValue() bool { return v.kind == KindBoolean && v.i != 0 }

// IntValue returns an integer's value, 0 for any other kind.
func (v Value) IntValue() int64 {
	if v.kind != KindInteger {
		return 0
	}
	return v.i
}

// DoubleValue returns a double's value, an integer's as a double (rounded), 0 otherwise.
func (v Value) DoubleValue() float64 {
	switch v.kind {
	case KindDouble:
		return v.f
	case KindInteger:
		return float64(v.i)
	}
	return 0
}

// TimestampValue returns a timestamp's seconds and nanoseconds.
func (v Value) TimestampValue() (seconds int64, nanos int32) {
	if v.kind != KindTimestamp {
		return 0, 0
	}
	return v.i, v.n
}

// StringValue returns a string's value, or a reference's name; "" otherwise.
func (v Value) StringValue() string {
	if v.kind != KindString && v.kind != KindReference {
		return ""
	}
	return v.s
}

// BytesValue returns a copy of a bytes value; nil otherwise.
func (v Value) BytesValue() []byte {
	if v.kind != KindBytes {
		return nil
	}
	return []byte(v.s)
}

// GeoPointValue returns a geopoint's latitude and longitude.
func (v Value) GeoPointValue() (lat, lng float64) {
	if v.kind != KindGeoPoint {
		return 0, 0
	}
	return v.f, v.g
}

// Elements returns an array's elements; the slice must not be modified.
func (v Value) Elements() []Value {
	if v.kind != KindArray {
		return nil
	}
	return v.list
}

// Fields returns a map's fields, sorted by name; the slice must not be modified.
func (v Value) Fields() []Field {
	if v.kind != KindMap {
		return nil
	}
	return v.m
}

// Get returns the field of a map named name.
func (v Value) Get(name string) (Value, bool) {
	if v.kind != KindMap {
		return Value{}, false
	}
	i, ok := slices.BinarySearchFunc(v.m, name, func(f Field, n string) int { return strings.Compare(f.Name, n) })
	if !ok {
		return Value{}, false
	}
	return v.m[i].Value, true
}

// IsVector reports whether v is a vector: a map of exactly __type__ = "__vector__" and value,
// an array of doubles. Any other map holding __type__ is refused by Validate. Like a BSON shape,
// a vector is one value to field masks: a mask path does not reach inside it.
func (v Value) IsVector() bool {
	if v.kind != KindMap || len(v.m) != 2 || v.m[0].Name != vectorTypeKey || v.m[1].Name != vectorValueKey {
		return false
	}
	if t := v.m[0].Value; t.kind != KindString || t.s != vectorTypeValue {
		return false
	}
	arr := v.m[1].Value
	if arr.kind != KindArray {
		return false
	}
	for _, e := range arr.list {
		if e.kind != KindDouble {
			return false
		}
	}
	return true
}

// VectorValues returns a vector's elements.
func (v Value) VectorValues() ([]float64, bool) {
	if !v.IsVector() {
		return nil, false
	}
	els := v.m[1].Value.list
	out := make([]float64, len(els))
	for i, e := range els {
		out[i] = e.f
	}
	return out, true
}

// vectorElements returns a vector's elements, which IsVector has checked are doubles.
func (v Value) vectorElements() []Value { return v.m[1].Value.list }

// TruncateMicros returns a timestamp with its nanoseconds rounded down to microseconds, as
// production stores it; any other value is returned as it is.
func (v Value) TruncateMicros() Value {
	if v.kind == KindTimestamp {
		v.n -= v.n % 1000
	}
	return v
}

// String renders v for messages and test failures: not a wire format.
func (v Value) String() string {
	var b strings.Builder
	v.write(&b)
	return b.String()
}

func (v Value) write(b *strings.Builder) {
	switch v.kind {
	case KindNull:
		b.WriteString("null")
	case KindBoolean:
		b.WriteString(strconv.FormatBool(v.i != 0))
	case KindInteger:
		b.WriteString(strconv.FormatInt(v.i, 10))
	case KindDouble:
		s := strconv.FormatFloat(v.f, 'g', -1, 64)
		if math.Signbit(v.f) && v.f == 0 {
			s = "-0"
		}
		if !strings.ContainsAny(s, ".eEnN") {
			s += ".0"
		}
		b.WriteString(s)
	case KindTimestamp:
		b.WriteString("timestamp(" + strconv.FormatInt(v.i, 10) + "s+" + strconv.Itoa(int(v.n)) + "ns)")
	case KindString:
		b.WriteString(strconv.Quote(v.s))
	case KindBytes:
		b.WriteString("bytes(" + base64.StdEncoding.EncodeToString([]byte(v.s)) + ")")
	case KindReference:
		b.WriteString("ref(" + v.s + ")")
	case KindGeoPoint:
		b.WriteString("geo(" + strconv.FormatFloat(v.f, 'g', -1, 64) + "," + strconv.FormatFloat(v.g, 'g', -1, 64) + ")")
	case KindArray:
		b.WriteByte('[')
		for i, e := range v.list {
			if i > 0 {
				b.WriteByte(',')
			}
			e.write(b)
		}
		b.WriteByte(']')
	case KindMap:
		b.WriteByte('{')
		for i, f := range v.m {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strconv.Quote(f.Name) + ":")
			f.Value.write(b)
		}
		b.WriteByte('}')
	}
}
