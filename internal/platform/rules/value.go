package rules

import (
	"fmt"
	"math"
	"slices"
	"strings"
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

// kindNames are the names the official messages give the types ("Received: [int]",
// "Received: set.difference(list)", "map.diff(string)"), the same in Pyric's recordings of the
// Rules Test API and hidane's of the emulator; "is" takes them too (typeIs).
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
// Expected: [bool].") and as "is" names it.
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
	//	KindString               string, WTF-8 (wtf8.go)
	//	KindBytes                string, the raw bytes
	//	KindList, KindSet        []Value; a set's members are distinct by E4, in the order added
	//	KindMap                  *mapValue
	//	KindMapDiff              *mapDiff
	//	KindTimestamp            time.Time, in UTC
	//	KindDuration             duration
	//	KindLatLng               latLng
	//	KindPath                 []string, decoded segments
	x any
}

// mapValue is a map with the order its entries were written in. values() returns them in that
// order, and keys() sorted (Pyric, required-fields-and-mapdiff "keyOrder"), so the order is part
// of the value. A map the services build with Map has its keys in sorted order.
type mapValue struct {
	keys    []string // each key of m once, in the order written
	entries map[string]Value
}

// duration is a rules duration, which reaches ±10,000 years: more than time.Duration holds.
type duration struct {
	seconds int64
	nanos   int32 // the same sign as seconds, |nanos| < 1e9
}

// latLng is a point in degrees.
type latLng struct{ lat, lng float64 }

// mapDiff is what left.diff(right) returns: the keys of left alone, of right alone, of both with
// values that differ, and of both with values that do not (E4), each sorted.
type mapDiff struct{ added, removed, changed, unchanged []string }

// Null returns null.
func Null() Value { return Value{kind: KindNull} }

// Bool returns a boolean.
func Bool(b bool) Value { return Value{kind: KindBool, x: b} }

// Int returns an integer.
func Int(i int64) Value { return Value{kind: KindInt, x: i} }

// Float returns a float.
func Float(f float64) Value { return Value{kind: KindFloat, x: f} }

// String returns a string. s is WTF-8: UTF-8, or UTF-8 holding lone surrogates in their three-byte
// form (wtf8.go); lengths and indexes count UTF-16 code units.
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

// Map returns a map of m, which it takes ownership of. Its entries are in the order of their
// keys, which is the order values() returns them in.
func Map(m map[string]Value) Value {
	if m == nil {
		m = map[string]Value{}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return Value{kind: KindMap, x: &mapValue{keys: keys, entries: m}}
}

// MapInOrder returns a map of the keys and values given, in that order, which is the order
// values() returns them in: a service whose source keeps an order gives it, as a Storage
// object's metadata keeps the order the request sent (Rules Test API, Pyric
// upload-primitives-boundaries "metadata values() keeps the order the request sent"). keys()
// is sorted whatever the order. A key given twice keeps its first place and its last value. It
// panics when keys and vals differ in length, a bug of the caller (buildMap's error for a
// literal).
func MapInOrder(keys []string, vals []Value) Value {
	if len(keys) != len(vals) {
		panic(fmt.Sprintf("rules: MapInOrder with %d keys and %d values", len(keys), len(vals)))
	}
	n := len(keys)
	order := make([]string, 0, n)
	entries := make(map[string]Value, n)
	for i := range n {
		if _, seen := entries[keys[i]]; !seen {
			order = append(order, keys[i])
		}
		entries[keys[i]] = vals[i]
	}
	return orderedMap(order, entries)
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
	m, ok := v.x.(*mapValue)
	if !ok || v.kind != KindMap {
		return nil, false
	}
	return m.entries, true
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

// The library reads values through these: the caller has checked the kind.

func (v Value) str() string        { s, _ := v.x.(string); return s }
func (v Value) items() []Value     { l, _ := v.x.([]Value); return l }
func (v Value) mapv() *mapValue    { m, _ := v.x.(*mapValue); return m }
func (v Value) segments() []string { p, _ := v.x.([]string); return p }
func (v Value) integer() int64     { i, _ := v.x.(int64); return i }
func (v Value) float() float64     { f, _ := v.x.(float64); return f }
func (v Value) boolean() bool      { b, _ := v.x.(bool); return b }
func (v Value) dur() duration      { d, _ := v.x.(duration); return d }
func (v Value) instant() time.Time { t, _ := v.x.(time.Time); return t }
func (v Value) point() latLng      { p, _ := v.x.(latLng); return p }
func (v Value) diff() *mapDiff     { d, _ := v.x.(*mapDiff); return d }
func (v Value) isNumber() bool     { return v.kind == KindInt || v.kind == KindFloat }
func (v Value) isReserved() bool   { return v.kind == KindConstraint || v.kind == KindUnknown }

// number is an int's or a float's value as a float.
func (v Value) number() float64 {
	if v.kind == KindInt {
		return float64(v.integer())
	}
	return v.float()
}

// setOf returns a set of members already distinct by E4.
func setOf(members []Value) Value {
	if members == nil {
		members = []Value{}
	}
	return Value{kind: KindSet, x: members}
}

// orderedMap returns a map of keys, each once, and their entries.
func orderedMap(keys []string, entries map[string]Value) Value {
	return Value{kind: KindMap, x: &mapValue{keys: keys, entries: entries}}
}

// pathOf returns a path of segs, which it takes ownership of.
func pathOf(segs []string) Value { return Value{kind: KindPath, x: segs} }

// The sizes the meter charges for what a builder allocates, besides the bytes of strings: one
// element of a list or a set, and one entry of a map with its key's header. Values' own headers
// are what Go spends on them, rounded up.
const (
	elemBytes  = 32
	entryBytes = 64
)

// chargeString checks and charges a string or bytes value of n bytes about to be built.
func chargeString(m *meter, n int) error {
	if err := m.value(int64(n), 0); err != nil {
		return err
	}
	return m.alloc(int64(n))
}

// chargeList checks and charges a list or a set of n elements about to be built.
func chargeList(m *meter, n int) error {
	if err := m.value(0, int64(n)); err != nil {
		return err
	}
	return m.alloc(int64(n) * elemBytes)
}

// chargeMap checks and charges a map of n entries about to be built, keyBytes being the bytes
// of keys it copies.
func chargeMap(m *meter, n int, keyBytes int64) error {
	if err := m.value(0, int64(n)); err != nil {
		return err
	}
	return m.alloc(int64(n)*entryBytes + keyBytes)
}

// newString charges and returns a string of s, which the caller has built already from parts
// whose sizes it charged; use it only where building costs no more than the string itself.
func newString(m *meter, s string) (Value, error) {
	if err := chargeString(m, len(s)); err != nil {
		return Value{}, err
	}
	return String(s), nil
}

// buildList returns a list literal's value: [a, b]. The interpreter (S1-C) calls it with the
// elements it evaluated, so the list is charged before it is kept.
func buildList(m *meter, elems []Value) (Value, error) {
	if err := chargeList(m, len(elems)); err != nil {
		return Value{}, err
	}
	return List(slices.Clone(elems)), nil
}

// buildMap returns a map literal's value: {k: v, …}. Keys must be strings ("Type error.
// Received: [int] Expected: [string].", Pyric simulator-language-semantics FS-SIM-11). A key
// written twice keeps the place of its first and the value of its last (Pyric keyOrder:
// "values() of a repeated key keeps its first position and last value").
func buildMap(m *meter, keys, vals []Value) (Value, error) {
	if len(keys) != len(vals) {
		return Value{}, errorf("a map literal has %d keys and %d values", len(keys), len(vals))
	}
	var keyBytes int64
	for _, k := range keys {
		if err := reserved(k); err != nil {
			return Value{}, err
		}
		if k.kind != KindString {
			return Value{}, errType(k.kind.String(), "string")
		}
		keyBytes += int64(len(k.str()))
	}
	if err := chargeMap(m, len(keys), keyBytes); err != nil {
		return Value{}, err
	}
	order := make([]string, 0, len(keys))
	entries := make(map[string]Value, len(keys))
	for i, k := range keys {
		name := k.str()
		if _, seen := entries[name]; !seen {
			order = append(order, name)
		}
		entries[name] = vals[i]
	}
	return orderedMap(order, entries), nil
}

// The official messages of evaluation errors, as both Pyric's recordings of the Rules Test API
// and hidane's of the emulator have them.

func errNull() *evalError { return errorf("Null value error.") }

func errProperty(name string) *evalError {
	return errorf("Property %s is undefined on object.", clip(name))
}

func errType(received, expected string) *evalError {
	return errorf("Type error. Received: [%s] Expected: [%s].", received, expected)
}

func errUnsupported(received string, expected []string) *evalError {
	return errorf("Unsupported operation error. Received: %s. Expected: %s.", received, strings.Join(expected, ", "))
}

func errNotFound(name string) *evalError {
	return errorf("Function not found error: Name: [%s].", clip(name))
}

func errArity(received int, expected []string) *evalError {
	return errorf("Incorrect number of arguments. Received: %d. Expected: %s.", received, strings.Join(expected, ", "))
}

func errIndex(index int64, size int) *evalError {
	return errorf("Index out of bound error. Index: [%d] , size: [%d].", index, size)
}

func errRange(from, to int64) *evalError {
	return errorf("Illegal range error. From index: [%d] , To index: [%d].", from, to)
}

func errOverflow() *evalError {
	return errorf("Integer value is out of the [-9223372036854775808, 9223372036854775807] range.")
}

func errDivideByZero() *evalError { return errorf("Divide by zero error.") }

func errConversion(argument string, from, to Kind) *evalError {
	return errorf("Type conversion error. Argument: [%s], From type: [%s], To type: [%s].", clip(argument), from, to)
}

func errRegex(pattern string) *evalError {
	return errorf("Invalid regular expression pattern. Pattern: %s.", clip(pattern))
}

func errTimestamp() *evalError {
	return errorf("Timestamp is not valid. Seconds must be in range [-62,135,596,800, +253,402,300,799]. Nanos must be in range [0, +999,999,999].")
}

func errDuration() *evalError {
	return errorf("Duration is not valid. Seconds must be in range [-315,576,000,000, +315,576,000,000]. Nanos must be in range [-999,999,999, +999,999,999]. Nanos must have the same sign as seconds.")
}

// errReserved is the error on a Constraint or an Unknown value, or on the zero Value: no
// message, a denial (ADR-021 §2.4).
func errReserved() *evalError { return &evalError{} }

// reserved returns errReserved for a value no operation may take: the reserved kinds and the
// zero Value.
func reserved(v Value) error {
	if v.isReserved() || v.kind == kindInvalid {
		return errReserved()
	}
	return nil
}

// typeNames lists the kinds of vs as the messages do: "int, string".
func typeNames(vs []Value) string {
	names := make([]string, len(vs))
	for i, v := range vs {
		names[i] = v.kind.String()
	}
	return strings.Join(names, ", ")
}

// signatures is the official text of a function's forms: name(int), name(float).
func signatures(name string, forms ...[]string) []string {
	out := make([]string, len(forms))
	for i, params := range forms {
		out[i] = fmt.Sprintf("%s(%s)", name, strings.Join(params, ", "))
	}
	return out
}

// mismatch is the error for a call of name that fits none of its forms: the wrong number of
// arguments when no form takes as many, wrong types otherwise.
func mismatch(name string, args []Value, forms ...[]string) *evalError {
	expected := signatures(name, forms...)
	for _, f := range forms {
		if len(f) == len(args) {
			return errUnsupported(fmt.Sprintf("%s(%s)", name, typeNames(args)), expected)
		}
	}
	return errArity(len(args), expected)
}
