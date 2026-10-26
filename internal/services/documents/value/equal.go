package value

import "math"

// Equal reports whether a and b match as production's equality filters match them: ==, in,
// array-contains (an element against the operand), array-contains-any, and the complement
// != / not-in. It is Compare(a, b) == 0 except that null and NaN (a Decimal128 NaN too) match
// nothing, themselves included (values-equality.json, values-bson.json): where v == null,
// in [null], array-contains null and the same with NaN return no document, while orderBy ties
// them. Inside an array or a map they match as usual ([null] == [null], {a: NaN} == {a: NaN}).
// MinKey is not null: == MinKey matches it, and != and IS_NOT_NULL include it.
//
// So -0.0 equals 0, 1 equals 1.0, int32 1 and the decimal 1.0, and a value the index cuts
// equals nothing short of the same cut. The SDKs never send == null or == NaN: they rewrite
// them to IS_NULL and IS_NAN. A query operand the index cannot hold whole (IndexedWhole) is
// refused before this is asked. One quirk is the query layer's, not Equal's: array-contains
// null (or NaN) matches the arrays that hold an empty array, and no other (values-equality.json,
// tr-empty-arrays-*).
func Equal(a, b Value) bool {
	if a.kind == KindNull || b.kind == KindNull || a.IsNaN() || b.IsNaN() {
		return false
	}
	return Compare(a, b) == 0
}

// Equivalent reports whether a and b are the same value as the array transforms see it
// (DocumentTransform.FieldTransform in write.proto): Compare's order without its index budget,
// so equivalent numbers of any type are one (3 and 3.0, -0.0 and 0), NaN is NaN and null is
// null, and arrays and maps are equivalent element by element and field by field. Unlike
// Compare, two long strings that differ past the index's 1500 bytes are not equivalent.
func Equivalent(a, b Value) bool {
	c := comparer{ra: math.MaxInt / 2, rb: math.MaxInt / 2}
	return c.value(a, b) == 0
}

// Identical reports whether a and b are the same value: the same kind, the same bits (-0.0 is
// not 0.0, any NaN is NaN), and for arrays and maps the same elements and fields, identical.
// It is what a write changes and a listener sees, where Equal is what a query matches.
func Identical(a, b Value) bool {
	if a.kind != b.kind {
		return false
	}
	switch a.kind {
	case KindNull:
		return true
	case KindBoolean, KindInteger:
		return a.i == b.i
	case KindDouble:
		return sameDouble(a.f, b.f)
	case KindTimestamp:
		return a.i == b.i && a.n == b.n
	case KindString, KindBytes, KindReference:
		return a.s == b.s
	case KindGeoPoint:
		return sameDouble(a.f, b.f) && sameDouble(a.g, b.g)
	case KindArray:
		if len(a.list) != len(b.list) {
			return false
		}
		for i := range a.list {
			if !Identical(a.list[i], b.list[i]) {
				return false
			}
		}
		return true
	}
	if len(a.m) != len(b.m) {
		return false
	}
	for i := range a.m {
		if a.m[i].Name != b.m[i].Name || !Identical(a.m[i].Value, b.m[i].Value) {
			return false
		}
	}
	return true
}

func sameDouble(x, y float64) bool {
	return math.Float64bits(x) == math.Float64bits(y) || math.IsNaN(x) && math.IsNaN(y)
}
