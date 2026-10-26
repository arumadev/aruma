package core

import (
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
// millisecond precision, rounded down. write.proto says "with millisecond precision", and
// production's transform results have whole milliseconds (f1-write-results.json
// rest.update_with_transform .976Z, grpc.update_with_transform nanos 857000000); that it is
// the commit_time rounded down, and not the time the request arrived, is this server's choice
// (every field of a commit gets the same one, as write.proto requires of a transaction).
func RequestTime(commitTime time.Time) value.Value {
	t := commitTime.Truncate(time.Millisecond)
	return value.Timestamp(t.Unix(), int32(t.Nanosecond()))
}

// apply returns a document's fields after the transform, and the transform's result: the
// field's new value, or null for the array kinds (write.proto). now is RequestTime's value.
func (t Transform) apply(fields, now value.Value) (out, result value.Value, err error) {
	base, found := lookup(fields, t.Path)
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
	var v value.Value
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
	out = value.ApplyMask(fields, nested(t.Path, v), []value.FieldPath{t.Path})
	if t.Kind == TransformAppendMissing || t.Kind == TransformRemoveAll {
		return out, value.Null(), nil
	}
	return out, v, nil
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
	for _, e := range add.Elements() {
		if !holds(out, e) {
			out = append(out, e)
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
	for _, e := range base.Elements() {
		if !holds(remove.Elements(), e) {
			out = append(out, e)
		}
	}
	return value.Array(out...)
}

func holds(els []value.Value, v value.Value) bool {
	return slices.ContainsFunc(els, func(e value.Value) bool { return value.Equivalent(e, v) })
}
