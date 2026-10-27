package core

import (
	"hash/maphash"
	"math"
	"slices"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

// TransformKind is what a field transform does (DocumentTransform.FieldTransform in write.proto).
type TransformKind uint8

const (
	// TransformServerTime sets the field to the commit's time (set_to_server_value REQUEST_TIME).
	TransformServerTime TransformKind = iota + 1
	// TransformIncrement adds Operand to the field (increment).
	TransformIncrement
	// TransformMaximum sets the field to the larger of it and Operand (maximum).
	TransformMaximum
	// TransformMinimum sets the field to the smaller of it and Operand (minimum).
	TransformMinimum
	// TransformAppendMissing appends Operand's elements the field's array lacks
	// (append_missing_elements, the SDKs' arrayUnion).
	TransformAppendMissing
	// TransformRemoveAll removes every element of the field's array that Operand holds
	// (remove_all_from_array, the SDKs' arrayRemove).
	TransformRemoveAll
)

// Transform is one field transform of a write. The transport has checked it: a field path that
// may be written, an integer or a double for the numeric kinds, an array for the array kinds.
type Transform struct {
	Path    value.FieldPath
	Kind    TransformKind
	Operand value.Value
}

// RequestTime is the value REQUEST_TIME gives a commit at commitTime: the commit_time at
// millisecond precision, rounded down. Production's value has whole milliseconds and is one
// value for the whole commit, as this is, but it is the request's time, a few milliseconds
// before the commitTime (f2-transforms.json st.*); this server takes the commit's own time on
// purpose, which is never after the commitTime and follows the commits' order (parity
// request-time-is-commit-time).
func RequestTime(commitTime time.Time) value.Value {
	t := commitTime.Truncate(time.Millisecond)
	return value.Timestamp(t.Unix(), int32(t.Nanosecond()))
}

// apply returns a document's fields after the transform, and the transform's result: the
// field's new value, or null for the array kinds (write.proto). now is RequestTime's value.
func (t Transform) apply(fields, now value.Value) (out, result value.Value, err error) {
	base, found := lookup(fields, t.Path)
	v, result, err := t.value(base, found, now)
	if err != nil {
		return value.Value{}, value.Value{}, err
	}
	return value.ApplyMask(fields, nested(t.Path, v), []value.FieldPath{t.Path}), result, nil
}

// applyTransforms is apply of each of a write's transforms in order: the fields after them all,
// and each one's result. The transport refuses a field transformed with one inside it, so two
// transforms' paths are equal or apart: a transform finds its field as the transforms of its
// path before it left it, or else as fields hold it, and the fields are rebuilt once, not once
// per transform, which would cost transforms × fields (F2 review M1).
func applyTransforms(fields value.Value, ts []Transform, now value.Value) (value.Value, []value.Value, error) {
	results := make([]value.Value, len(ts))
	after := make(map[string]value.Value, len(ts)) // by path: the field as the transforms so far left it
	var paths []value.FieldPath
	update := &updateTree{}
	for i, t := range ts {
		k := t.Path.String()
		base, found := after[k]
		if !found {
			base, found = lookup(fields, t.Path)
			paths = append(paths, t.Path)
		}
		v, result, err := t.value(base, found, now)
		if err != nil {
			return value.Value{}, nil, err
		}
		after[k], results[i] = v, result
	}
	for _, p := range paths {
		update.set(p, after[p.String()])
	}
	return value.ApplyMask(fields, update.value(), paths), results, nil
}

// updateTree is the update the transforms of a write make, by path segment: a node holds a value
// or the nodes below it.
type updateTree struct {
	v    *value.Value
	kids map[string]*updateTree
}

func (u *updateTree) set(p value.FieldPath, v value.Value) {
	for _, seg := range p {
		if u.kids == nil {
			u.kids = map[string]*updateTree{}
		}
		k := u.kids[seg]
		if k == nil {
			k = &updateTree{}
			u.kids[seg] = k
		}
		u = k
	}
	u.v = &v
}

func (u *updateTree) value() value.Value {
	if u.v != nil {
		return *u.v
	}
	fields := make([]value.Field, 0, len(u.kids))
	for name, k := range u.kids {
		fields = append(fields, value.Field{Name: name, Value: k.value()})
	}
	return value.Map(fields...)
}

// value returns the field's value after the transform, from base (found: the field exists), and
// the transform's result.
func (t Transform) value(base value.Value, found bool, now value.Value) (v, result value.Value, err error) {
	if t.Kind == TransformIncrement || t.Kind == TransformMaximum || t.Kind == TransformMinimum {
		// Standard refuses a numeric transform of an Enterprise BSON number (f2-transforms.json
		// bson.on_int32_field, bson.on_decimal_field).
		switch base.BSONKind() {
		case value.BSONInt32:
			return value.Value{}, value.Value{}, &Error{InvalidArgument, "Value type INT is not allowed"}
		case value.BSONDecimal128:
			return value.Value{}, value.Value{}, &Error{InvalidArgument, "Value type DECIMAL128 is not allowed"}
		}
	}
	switch t.Kind {
	case TransformServerTime:
		v = now
	case TransformIncrement:
		v = increment(base, found, t.Operand)
	case TransformMaximum:
		v = extreme(base, found, t.Operand, 1)
	case TransformMinimum:
		v = extreme(base, found, t.Operand, -1)
	case TransformAppendMissing:
		v = appendMissing(base, found, t.Operand)
	case TransformRemoveAll:
		v = removeAll(base, found, t.Operand)
	}
	if t.Kind == TransformAppendMissing || t.Kind == TransformRemoveAll {
		return v, value.Null(), nil
	}
	return v, v, nil
}

// lookup returns the value at p in fields as a transform finds its field: through maps alone. A
// vector or a BSON shape on the way is a value, not a map, as an update mask sees it
// (value.ApplyMask), so a path into one finds nothing.
func lookup(fields value.Value, p value.FieldPath) (value.Value, bool) {
	v := fields
	for _, seg := range p {
		if v.Kind() != value.KindMap || v.IsVector() || v.BSONKind() != value.NotBSON {
			return value.Value{}, false
		}
		f, ok := v.Get(seg)
		if !ok {
			return value.Value{}, false
		}
		v = f
	}
	return v, true
}

// nested returns the map that holds v at p, for value.ApplyMask.
func nested(p value.FieldPath, v value.Value) value.Value {
	for i := len(p) - 1; i >= 0; i-- {
		v = value.Map(value.Field{Name: p[i], Value: v})
	}
	return v
}

// increment adds n to the field (write.proto): a field that is missing or not an integer or a
// double becomes n; two integers add as integers, saturating at the int64 bounds on overflow;
// otherwise both are doubles and add as IEEE 754 doubles.
func increment(base value.Value, found bool, n value.Value) value.Value {
	if !found || !base.IsNumber() {
		return n
	}
	if base.Kind() == value.KindInteger && n.Kind() == value.KindInteger {
		a, b := base.IntValue(), n.IntValue()
		sum := a + b
		switch {
		case b > 0 && sum < a:
			return value.Int(math.MaxInt64)
		case b < 0 && sum > a:
			return value.Int(math.MinInt64)
		}
		return value.Int(sum)
	}
	return value.Double(base.DoubleValue() + n.DoubleValue())
}

// extreme is maximum (sign 1) and minimum (sign -1) (write.proto): a field that is missing or not
// an integer or a double becomes n; NaN on either side wins (the field's own first); otherwise
// the larger (smaller) of the two by numeric value, keeping its type, and the field when they are
// equal (3 and 3.0; 0, 0.0 and -0.0 alike).
func extreme(base value.Value, found bool, n value.Value, sign int) value.Value {
	switch {
	case !found || !base.IsNumber():
		return n
	case base.IsNaN():
		return base
	case n.IsNaN():
		return n
	case value.Compare(n, base)*sign > 0:
		return n
	}
	return base
}

// appendMissing appends to the field's array each element of add that it does not hold yet,
// in order, by value.Equivalent; an element equivalent to one before it in add is not appended
// twice. A field that is missing or not an array starts empty.
func appendMissing(base value.Value, found bool, add value.Value) value.Value {
	var els []value.Value
	if found && base.Kind() == value.KindArray {
		els = base.Elements()
	}
	out := slices.Clip(els)
	set := newElementSet(out, len(out)+len(add.Elements()), len(add.Elements()))
	for _, e := range add.Elements() {
		if !set.holds(e) {
			out = append(out, e)
			set.add(e)
		}
	}
	return value.Array(out...)
}

// removeAll removes from the field's array every element equivalent to one of remove
// (value.Equivalent), duplicates included. A field that is missing or not an array becomes [].
func removeAll(base value.Value, found bool, remove value.Value) value.Value {
	if !found || base.Kind() != value.KindArray {
		return value.Array()
	}
	var out []value.Value
	set := newElementSet(remove.Elements(), len(remove.Elements()), len(base.Elements()))
	for _, e := range base.Elements() {
		if !set.holds(e) {
			out = append(out, e)
		}
	}
	return value.Array(out...)
}

// elementSet answers whether an array holds an element equivalent to a value. Past a few
// elements it looks them up by a hash of their whole key (value.AppendWholeKey), which two
// values share exactly when they are equivalent, so a bucket holds only equivalent elements
// but where hashes collide, and Equivalent decides among them. An array transform then costs
// the elements of both arrays, not their product (F2 review M1), whatever their types and
// however long (F2 re-review N2: the index's sort key cuts long values to a shared prefix, and
// spends its budget differently on an int32, an integer and a decimal that are equivalent).
type elementSet struct {
	els    []value.Value
	byHash map[uint64][]value.Value
	seed   maphash.Seed
	key    []byte // scratch for a whole key
}

// linearElements is the product of a set's size and its probes below which a scan is cheaper
// than hashing.
const linearElements = 64

// newElementSet returns the set of els, which will hold at most most elements and be asked about
// probes values.
func newElementSet(els []value.Value, most, probes int) *elementSet {
	s := &elementSet{els: els}
	if most*probes > linearElements {
		s.byHash = make(map[uint64][]value.Value, len(els))
		s.seed = maphash.MakeSeed()
		for _, e := range els {
			s.add(e)
		}
	}
	return s
}

func (s *elementSet) hash(v value.Value) uint64 {
	s.key = value.AppendWholeKey(s.key[:0], v)
	return maphash.Bytes(s.seed, s.key)
}

func (s *elementSet) add(v value.Value) {
	if s.byHash == nil {
		s.els = append(s.els, v)
		return
	}
	h := s.hash(v)
	s.byHash[h] = append(s.byHash[h], v)
}

func (s *elementSet) holds(v value.Value) bool {
	els := s.els
	if s.byHash != nil {
		els = s.byHash[s.hash(v)]
	}
	return slices.ContainsFunc(els, func(e value.Value) bool { return value.Equivalent(e, v) })
}
