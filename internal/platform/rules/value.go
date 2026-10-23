package rules

import (
	"math"
	"time"
)

// Kind is the type of a Value.
type Kind uint8

const (
	kindInvalid Kind = iota // the zero Value, which is not a value: every operation on it fails

	KindNull
	KindBool
	KindInt   // int64
	KindFloat // float64
	KindString
	KindBytes
	KindList
	KindMap
	KindSet     // built by toSet(); its members are distinct by identity of a set (E4)
	KindMapDiff // built by diff()
	KindTimestamp
	KindDuration
	KindLatLng
	KindPath

	// Reserved for Firestore's list queries (F5, ADR-021 §2.4). No constructor makes them in S1,
	// and every operation that meets one is an error without a message, so the engine never
	// allows what it does not understand.
	KindConstraint // what a query knows of a field: pairs of comparator and value
	KindUnknown    // what cannot be evaluated at all, such as request.path in a collection group
)

var kindNames = [...]string{
	kindInvalid:    "invalid",
	KindNull:       "null",
	KindBool:       "bool",
	KindInt:        "int",
	KindFloat:      "float",
	KindString:     "string",
	KindBytes:      "bytes",
	KindList:       "list",
	KindMap:        "map",
	KindSet:        "set",
	KindMapDiff:    "map_diff",
	KindTimestamp:  "timestamp",
	KindDuration:   "duration",
	KindLatLng:     "latlng",
	KindPath:       "path",
	KindConstraint: "constraint",
	KindUnknown:    "unknown",
}

// String returns the type's name as the messages write it ("Type error. Received: [int]
// Expected: [bool].") and as "is" names it. S1-B checks the names against the oracle.
func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "invalid"
}

// Value is a value of the rules language. Values are immutable: constructors that take a slice
// or a map take ownership of it, and readers that return one return the value's own, which the
// caller must not change. The zero Value is not a value: every operation on it is an error.
//
// Values cannot be compared with ==, which would panic on a list or a map: the language's four
// equality relations are functions of their own (equal.go).
type Value struct {
	_    [0]func()
	kind Kind
	// x holds the kind's data:
	//
	//	KindNull                 nil
	//	KindBool                 bool
	//	KindInt                  int64
	//	KindFloat                float64
	//	KindString               string, UTF-8
	//	KindBytes                string, the raw bytes
	//	KindList, KindSet        []Value
	//	KindMap                  map[string]Value
	//	KindMapDiff              *mapDiff
	//	KindTimestamp            time.Time, in UTC
	//	KindDuration             duration
	//	KindLatLng               latLng
	//	KindPath                 []string, decoded segments
	x any
}

// duration is a rules duration, which reaches ±10,000 years: more than time.Duration holds.
type duration struct {
	seconds int64
	nanos   int32 // the same sign as seconds, |nanos| < 1e9
}

// latLng is a point in degrees.
type latLng struct{ lat, lng float64 }

// mapDiff is what left.diff(right) returns.
type mapDiff struct{ left, right map[string]Value }

// Null returns null.
func Null() Value { return Value{kind: KindNull} }

// Bool returns a boolean.
func Bool(b bool) Value { return Value{kind: KindBool, x: b} }

// Int returns an integer.
func Int(i int64) Value { return Value{kind: KindInt, x: i} }

// Float returns a float.
func Float(f float64) Value { return Value{kind: KindFloat, x: f} }

// String returns a string. s is UTF-8; lengths and indexes count UTF-16 code units.
func String(s string) Value { return Value{kind: KindString, x: s} }

// Bytes returns a bytes value holding a copy of b.
func Bytes(b []byte) Value { return Value{kind: KindBytes, x: string(b)} }

// List returns a list of vs, which it takes ownership of.
func List(vs []Value) Value {
	if vs == nil {
		vs = []Value{}
	}
	return Value{kind: KindList, x: vs}
}

// Map returns a map of m, which it takes ownership of.
func Map(m map[string]Value) Value {
	if m == nil {
		m = map[string]Value{}
	}
	return Value{kind: KindMap, x: m}
}

// Timestamp returns a timestamp.
func Timestamp(t time.Time) Value { return Value{kind: KindTimestamp, x: t.UTC()} }

// Duration returns a duration.
func Duration(d time.Duration) Value {
	return Value{kind: KindDuration, x: duration{seconds: int64(d / time.Second), nanos: int32(d % time.Second)}}
}

// LatLng returns a point at lat and lng degrees.
func LatLng(lat, lng float64) Value { return Value{kind: KindLatLng, x: latLng{lat, lng}} }

// PathOf returns a path of the decoded segments segs, copied.
func PathOf(segs []string) Value {
	return Value{kind: KindPath, x: append([]string{}, segs...)}
}

// Kind returns the value's type.
func (v Value) Kind() Kind { return v.kind }

// AsBool returns a boolean's value; ok is false for any other kind.
func (v Value) AsBool() (b, ok bool) {
	b, ok = v.x.(bool)
	return b, ok && v.kind == KindBool
}

// AsInt returns an integer's value; ok is false for any other kind.
func (v Value) AsInt() (int64, bool) {
	i, ok := v.x.(int64)
	return i, ok && v.kind == KindInt
}

// AsFloat returns a float's value; ok is false for any other kind, ints included.
func (v Value) AsFloat() (float64, bool) {
	f, ok := v.x.(float64)
	return f, ok && v.kind == KindFloat
}

// AsString returns a string's value; ok is false for any other kind.
func (v Value) AsString() (string, bool) {
	s, ok := v.x.(string)
	return s, ok && v.kind == KindString
}

// AsBytes returns a copy of a bytes value; ok is false for any other kind.
func (v Value) AsBytes() ([]byte, bool) {
	s, ok := v.x.(string)
	if !ok || v.kind != KindBytes {
		return nil, false
	}
	return []byte(s), true
}

// AsList returns a list's elements, which the caller must not change; ok is false for any other
// kind, sets included.
func (v Value) AsList() ([]Value, bool) {
	l, ok := v.x.([]Value)
	return l, ok && v.kind == KindList
}

// AsMap returns a map's entries, which the caller must not change; ok is false for any other
// kind.
func (v Value) AsMap() (map[string]Value, bool) {
	m, ok := v.x.(map[string]Value)
	return m, ok && v.kind == KindMap
}

// AsTimestamp returns a timestamp's time, in UTC; ok is false for any other kind.
func (v Value) AsTimestamp() (time.Time, bool) {
	t, ok := v.x.(time.Time)
	return t, ok && v.kind == KindTimestamp
}

// AsDuration returns a duration; ok is false for any other kind, and for a duration beyond
// time.Duration's ±292 years.
func (v Value) AsDuration() (time.Duration, bool) {
	d, ok := v.x.(duration)
	if !ok || v.kind != KindDuration {
		return 0, false
	}
	// time.Duration spans MinInt64 to MaxInt64 nanoseconds: whole seconds up to maxSeconds, and
	// at ±maxSeconds only the nanoseconds left over.
	const (
		maxSeconds = math.MaxInt64 / int64(time.Second)
		maxNanos   = math.MaxInt64 % int64(time.Second) // 854775807
		minNanos   = math.MinInt64 % int64(time.Second) // -854775808
	)
	switch {
	case d.seconds > maxSeconds || d.seconds < -maxSeconds,
		d.seconds == maxSeconds && int64(d.nanos) > maxNanos,
		d.seconds == -maxSeconds && int64(d.nanos) < minNanos:
		return 0, false
	}
	return time.Duration(d.seconds)*time.Second + time.Duration(d.nanos), true
}

// AsLatLng returns a point's degrees; ok is false for any other kind.
func (v Value) AsLatLng() (lat, lng float64, ok bool) {
	p, ok := v.x.(latLng)
	return p.lat, p.lng, ok && v.kind == KindLatLng
}

// AsPath returns a copy of a path's decoded segments; ok is false for any other kind.
func (v Value) AsPath() ([]string, bool) {
	p, ok := v.x.([]string)
	if !ok || v.kind != KindPath {
		return nil, false
	}
	return append([]string{}, p...), true
}
