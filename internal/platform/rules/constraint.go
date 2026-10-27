package rules

import (
	"maps"
	"math"
	"slices"
	"strings"
)

// What a list query knows of a document (ADR-021 §2.4). A list is decided before any document
// is read: Firestore's binding splits the query into ways (each branch of an or, each value of
// an in and of an array-contains-any) and evaluates each way with Evaluate on allow list. In a
// way, resource.data is a PartialMap whose fields are what the way's filters say: a Constraint
// on a field the filters name, a nested PartialMap for a field path a.b, and nothing else.
//
// The model is the emulator's as hidane measured it (ADR 0005, #66), corrected by production
// (f5-rules-queries.json, f5-rules-constraints.json): a comparison on a Constraint is true when
// one of the field's filters alone implies it for every document the filter matches, and
// otherwise an error without a message, never false. n >= 1 and n <= 1 together do not imply
// n == 1, and a filter that matches nothing implies every comparison. Production's own rules:
//
//   - a range implies a range whose bound it falls inside, never one whose edge it reaches past
//     (n <= 10 does not imply n < 10; n >= 0 does not imply n > 0, c.*, d.*), and only when the
//     two bounds are numbers of one kind (n < 5.0 does not imply n < 10, c.lt-5-double); a
//     range never matches NaN (value.InRange), so n < 5 implies n < 10;
//   - an equality implies a comparison by value (n == 3.0 implies n == 3, z == -0.0 implies
//     z > -1, g.*, h.*), and != of a value of any other type (x == 'a' implies x != null);
//   - an equality makes "field in list" true when the list holds its value, a number of the
//     same kind (x == 1 implies x in [1, 2], q10.*), as production allows though the rules'
//     in finds no 1.0 in [1, 2];
//   - a single-value in and an equality are one: the binding splits in into ways of ==.
//
// Where nothing is measured the answer is the stricter one, an error: the engine never allows
// more than Firebase. Leaving a filter out of a Constraint, or a field out of a PartialMap,
// never allows more.

// Comparator is a list query filter's operator.
type Comparator uint8

const (
	FilterEQ          Comparator = iota + 1 // ==
	FilterNE                                // !=
	FilterLT                                // <
	FilterLE                                // <=
	FilterGT                                // >
	FilterGE                                // >=
	FilterNotIn                             // not-in; the Value is a list
	FilterContains                          // array-contains
	FilterIn                                // in; the binding splits it into ways of FilterEQ, so it implies nothing
	FilterContainsAny                       // array-contains-any; split into ways of FilterContains, so it implies nothing
)

// Filter is one filter of a list query on a field: the documents whose field compares to Value
// by Op, as Firestore compares (numbers by value, strings by UTF-8 bytes, NaN below every
// number).
type Filter struct {
	Op    Comparator
	Value Value
}

// Constraint returns what a list query's filters say of one field. Of the operations, only ==,
// !=, <, <=, >, >=, "x in field" and "field in list" take it (constrained); every other one is
// an error without a message.
func Constraint(filters ...Filter) Value {
	return Value{kind: KindConstraint, x: slices.Clone(filters)}
}

// Unknown returns a value that cannot be evaluated at all, such as request.path in a collection
// group query: every operation on it is an error without a message, and so is reading it from a
// map.
func Unknown() Value { return Value{kind: KindUnknown} }

// PartialMap returns a map of which only fields are known: resource.data in a list query, or a
// map field a filter names a field of. Reading a field it holds gives that field, reading any
// other is an error without a message, and every other operation on it (size, keys, in, ==, is,
// get) is too. It takes ownership of fields. A field may hold a Constraint, a PartialMap, or a
// value, which claims every document the way matches holds exactly that value.
func PartialMap(fields map[string]Value) Value {
	if fields == nil {
		fields = map[string]Value{}
	}
	return Value{kind: KindUnknown, x: &mapValue{keys: slices.Sorted(maps.Keys(fields)), entries: fields}}
}

// Query returns request.query for a list, as production shows it (f5-rules-queries.json q2 to
// q13): limit an int, or null when the query has none; offset an int, 0 when it has none
// (offset <= 1 allows a query without one); orderBy a list of orders explicitly given, whose
// size is known and whose elements are Unknown (orderBy[0] == 'x' and orderBy.x are denied).
// The three keys are always there ('limit' in request.query).
func Query(limit *int64, offset int64, orders int) Value {
	q := map[string]Value{"limit": Null(), "offset": Int(offset)}
	if limit != nil {
		q["limit"] = Int(*limit)
	}
	ob := make([]Value, orders)
	for i := range ob {
		ob[i] = Unknown()
	}
	q["orderBy"] = List(ob)
	return Map(q)
}

func (v Value) filters() []Filter { f, _ := v.x.([]Filter); return f }

// partial returns the known fields of a PartialMap, and false for any other value.
func (v Value) partial() (*mapValue, bool) {
	if v.kind != KindUnknown {
		return nil, false
	}
	mv, ok := v.x.(*mapValue)
	return mv, ok
}

// partialField returns field name of a PartialMap: an error without a message when it is not
// known, or when it holds an Unknown.
func partialField(mv *mapValue, name string) (Value, error) {
	v, ok := mv.entries[name]
	if !ok || v.kind == kindInvalid || v.kind == KindUnknown && v.x == nil {
		return Value{}, errReserved()
	}
	return v, nil
}

// constrained evaluates x op y when x or y is a Constraint: true when one of the Constraint's
// filters implies it, an error without a message otherwise.
func constrained(m *meter, op token, x, y Value) (Value, error) {
	if x.kind == KindConstraint && y.kind == KindConstraint || reserved(x) != nil && x.kind != KindConstraint ||
		reserved(y) != nil && y.kind != KindConstraint {
		return Value{}, errReserved()
	}
	c, b := x, y
	switch op {
	case tokIn:
		// b in c, or c in a list or a set (c, b = x, y).
		if y.kind == KindConstraint {
			c, b = y, x
		} else if y.kind != KindList && y.kind != KindSet {
			return Value{}, errReserved()
		} else {
			op = tokInList
		}
	case tokEQ, tokNE, tokLT, tokLE, tokGT, tokGE:
		if y.kind == KindConstraint {
			c, b, op = y, x, flip(op)
		}
	default:
		return Value{}, errReserved()
	}
	fs := c.filters()
	if err := m.work(int64(len(fs))); err != nil {
		return Value{}, err
	}
	for _, f := range fs {
		if op == tokInList && inList(m, f, b) || op != tokInList && implies(f, op, b) {
			return Bool(true), nil
		}
	}
	return Value{}, errReserved()
}

// flip turns b op c into c op' b.
func flip(op token) token {
	switch op {
	case tokLT:
		return tokGT
	case tokLE:
		return tokGE
	case tokGT:
		return tokLT
	case tokGE:
		return tokLE
	}
	return op // == and != are symmetric
}

// tokInList is "v in b" for a list or a set b, which constrained tells from tokIn, "b in v".
const tokInList = token(255)

// inList reports whether every value v that filter f matches is in the list or set b. Every
// match of an equality equals its value a, and is a itself as far as in can tell, but for a
// number of the other kind: production allows x == 1 for a rule x in [1, 2] (q10.eq-1), though
// the rules' in (E3) finds no 1.0 in [1, 2].
func inList(m *meter, f Filter, b Value) bool {
	if f.Op != FilterEQ || classOf(f.Value) == vcNone {
		return false
	}
	r, err := binary(m, tokIn, f.Value, b)
	return err == nil && r.kind == KindBool && r.boolean()
}

// implies reports whether every value v that filter f matches makes "v op b" true, where op is
// a comparison, or tokIn for "b in v".
func implies(f Filter, op token, b Value) bool {
	if op == tokIn {
		// array-contains a: every array holds an element Firestore finds equal to a. in compares
		// a list's elements by E3, where an int is not a float, so numbers are left out, and
		// containers, whose numbers Firestore compares by value.
		a := f.Value
		return f.Op == FilterContains && a.kind == b.kind && containable(a) && sameScalar(a, b)
	}
	if matchesNothing(f) {
		return true // measured: a filter that matches nothing implies == true and == false
	}
	a := f.Value
	ca, cb := classOf(a), classOf(b)
	switch f.Op {
	case FilterEQ:
		// Every match equals a (an int or a float of a's value, either zero for a zero), and
		// the rules compare it to b as they compare a.
		switch {
		case ca == vcNone || cb == vcNone:
			return false
		case ca != cb:
			return op == tokNE // a value of another type is never b (x == 'a' implies x != null)
		case op == tokEQ:
			return sameScalar(a, b)
		case op == tokNE:
			return !sameScalar(a, b)
		case ca == vcNull || ca == vcBool:
			return false // the rules do not order them
		}
		c := orderOf(a, b)
		if ca == vcString {
			c = compareUTF16(a.str(), b.str())
		}
		switch op {
		case tokLT:
			return c < 0
		case tokLE:
			return c <= 0
		case tokGT:
			return c > 0
		}
		return c >= 0
	case FilterNE:
		return op == tokNE && ca != vcNone && ca == cb && !isZero(b) && sameScalar(a, b)
	case FilterNotIn:
		if op != tokNE || a.kind != KindList || cb == vcNone || isZero(b) {
			return false
		}
		return slices.ContainsFunc(a.items(), func(e Value) bool { return classOf(e) == cb && sameScalar(e, b) })
	case FilterLT, FilterLE, FilterGT, FilterGE:
		return rangeImplies(f.Op, op, a, b, ca, cb)
	}
	return false
}

// rangeImplies reports whether every v with "v fop a" makes "v op b" true. Only values of one
// order the rules share with Firestore take part: numbers within ±2^53 (exact as floats) or
// infinite, both ints or both floats (c.lt-5-double), bytes, timestamps, and strings when b's
// characters all precede U+D800, where the rules' UTF-16 order and Firestore's UTF-8 order
// agree. Firestore's ranges never match NaN.
func rangeImplies(fop Comparator, op token, a, b Value, ca, cb valueClass) bool {
	if ca != cb || ca == vcNone || ca == vcNull || ca == vcBool || ca == vcNumber && a.kind != b.kind {
		return false
	}
	if cb == vcString && !belowSurrogates(b.str()) {
		return false
	}
	below := fop == FilterLT || fop == FilterLE
	if isZero(a) && isZero(b) && op != tokLE && op != tokGE &&
		(fop != FilterGT || a.kind == KindFloat && math.Signbit(a.float())) {
		// Whether Firestore orders -0.0 below 0.0 is unmeasured: v > 0 is never a zero, but
		// v > -0.0 and v < 0 may be.
		return false
	}
	c := orderOf(a, b)
	strict := fop == FilterGT || fop == FilterLT // v is never a
	switch {
	case !below && (op == tokGT || op == tokGE || op == tokNE):
		// v > a or v >= a: v > b when a >= b (strict filter) or a > b; v >= b when a >= b.
		return c > 0 || c == 0 && (strict || op == tokGE)
	case below && (op == tokLT || op == tokLE || op == tokNE):
		return c < 0 || c == 0 && (strict || op == tokLE)
	}
	return false
}

// valueClass is a kind of value implication compares, or vcNone for a value it takes no
// part in: a container, a NaN, a number beyond ±2^53, or a kind Firestore does not store.
type valueClass uint8

const (
	vcNone valueClass = iota
	vcNull
	vcBool
	vcNumber
	vcString
	vcBytes
	vcTimestamp
)

// exactLimit is 2^53: every int of smaller magnitude is exact as a float.
const exactLimit = 1 << 53

func classOf(v Value) valueClass {
	switch v.kind {
	case KindNull:
		return vcNull
	case KindBool:
		return vcBool
	case KindInt:
		if i := v.integer(); i > -exactLimit && i < exactLimit {
			return vcNumber
		}
	case KindFloat:
		if f := v.float(); math.IsInf(f, 0) || f > -exactLimit && f < exactLimit {
			return vcNumber
		}
	case KindString:
		return vcString
	case KindBytes:
		return vcBytes
	case KindTimestamp:
		return vcTimestamp
	}
	return vcNone
}

// containable reports whether "b in list" finds b where Firestore's array-contains does.
func containable(v Value) bool {
	switch v.kind {
	case KindNull, KindBool, KindString, KindBytes, KindTimestamp:
		return true
	}
	return false
}

func isZero(v Value) bool { return v.isNumber() && v.number() == 0 }

// sameScalar reports whether a and b, of one class, are the same value.
func sameScalar(a, b Value) bool {
	if classOf(a) != classOf(b) || classOf(a) == vcNone {
		return false
	}
	return orderOf(a, b) == 0
}

// orderOf compares a and b of one class as Firestore orders them: strings by their UTF-8 bytes.
func orderOf(a, b Value) int {
	switch a.kind {
	case KindNull:
		return 0
	case KindBool:
		return compareInts(boolInt(a.boolean()), boolInt(b.boolean()))
	case KindString, KindBytes:
		return strings.Compare(a.str(), b.str())
	case KindTimestamp:
		return a.instant().Compare(b.instant())
	}
	x, y := a.number(), b.number()
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// belowSurrogates reports whether every character of s precedes U+D800.
func belowSurrogates(s string) bool {
	for _, r := range s {
		if r >= 0xD800 {
			return false
		}
	}
	return true
}

// matchesNothing reports whether no value can satisfy f: nothing of its type is below false,
// "" or b"", or above true (Firestore's inequalities match their own type only).
func matchesNothing(f Filter) bool {
	a := f.Value
	switch f.Op {
	case FilterLT:
		switch a.kind {
		case KindBool:
			return !a.boolean()
		case KindString, KindBytes:
			return a.str() == ""
		}
	case FilterGT:
		return a.kind == KindBool && a.boolean()
	}
	return false
}
