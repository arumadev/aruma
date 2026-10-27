package rules

import (
	"fmt"
	"math"
	"math/bits"
)

// The operators of the language (ADR-021 §2.3). Each charges m before it builds a value, and
// returns only an *evalError, which && and || may absorb, or an *abort, which ends the
// evaluation. Any kind an operator does not know, the reserved Constraint and Unknown included,
// is an *evalError.
//
// The kinds each operator takes, and its messages, are production's as Pyric recorded them
// (cross-type-operator-overloads, arithmetic-operand-types, ordering-operand-types,
// int-float-and-division), which agree with hidane's recordings of the emulator:
//
//	+   int + int, float + float (and an int with a float, as a float), string + string,
//	    duration + duration, duration + timestamp, timestamp + duration. Not list + list,
//	    bytes + bytes or map + map: those are errors ([1] + [2] == [1, 2] is denied).
//	-   int, float, duration - duration, timestamp - duration, timestamp - timestamp.
//	* / % ints or floats. An int divided by zero is an error, not Infinity; a float divided
//	    by zero is IEEE's.
//	< <= > >= strings (by UTF-16 code unit, as Java compares them), bytes, numbers, durations,
//	    timestamps. Anything else, bools and lists included, is an error.
//	== !=  any two values (E1, equal.go), never an error but on a reserved value.
//	in  a list (E3), a set (E4), a map's keys (strings only).

// binary applies x op y for every binary operator but && and ||, which the interpreter
// evaluates itself: + - * / % < <= > >= == != and in.
func binary(m *meter, op token, x, y Value) (Value, error) {
	if x.kind == KindConstraint || y.kind == KindConstraint {
		return constrained(m, op, x, y) // constraint.go
	}
	if err := reserved(x); err != nil {
		return Value{}, err
	}
	if err := reserved(y); err != nil {
		return Value{}, err
	}
	switch op {
	case tokEQ, tokNE:
		eq, err := equalTop(m, x, y)
		if err != nil {
			return Value{}, err
		}
		return Bool(eq == (op == tokEQ)), nil
	case tokLT, tokLE, tokGT, tokGE:
		return compare(m, op, x, y)
	case tokIn:
		return member(m, x, y)
	case tokPlus:
		return add(m, x, y)
	case tokMinus:
		return subtract(x, y)
	case tokStar, tokSlash, tokPercent:
		return multiply(op, x, y)
	}
	return Value{}, errorf("%s is not a binary operator", op)
}

// unsupportedOp is the official error for x op y of kinds op does not take.
func unsupportedOp(op token, x, y Value, forms ...[2]string) *evalError {
	expected := make([]string, len(forms))
	for i, f := range forms {
		expected[i] = fmt.Sprintf("%s %s %s", f[0], op, f[1])
	}
	return errUnsupported(fmt.Sprintf("%s %s %s", x.kind, op, y.kind), expected)
}

// compare orders x and y. NaN is neither less nor more than anything.
func compare(m *meter, op token, x, y Value) (Value, error) {
	var c int
	nan := false
	switch {
	case x.kind == KindString && y.kind == KindString:
		if err := m.work(walkWork(min(len(x.str()), len(y.str())))); err != nil {
			return Value{}, err
		}
		c = compareUTF16(x.str(), y.str())
	case x.kind == KindBytes && y.kind == KindBytes:
		if err := m.work(scanWork(min(len(x.str()), len(y.str())))); err != nil {
			return Value{}, err
		}
		c = compareStrings(x.str(), y.str())
	case x.kind == KindInt && y.kind == KindInt:
		c = compareInts(x.integer(), y.integer())
	case x.isNumber() && y.isNumber():
		a, b := x.number(), y.number()
		switch {
		case math.IsNaN(a) || math.IsNaN(b):
			nan = true
		case a < b:
			c = -1
		case a > b:
			c = 1
		}
	case x.kind == KindDuration && y.kind == KindDuration:
		c = compareDurations(x.dur(), y.dur())
	case x.kind == KindTimestamp && y.kind == KindTimestamp:
		c = x.instant().Compare(y.instant())
	default:
		return Value{}, unsupportedOp(op, x, y,
			[2]string{"string", "string"}, [2]string{"bytes", "bytes"}, [2]string{"int", "int"},
			[2]string{"float", "float"}, [2]string{"duration", "duration"},
			[2]string{"timestamp", "timestamp"}, [2]string{"constraint", "any"},
			[2]string{"any", "constraint"})
	}
	if nan {
		return Bool(false), nil
	}
	switch op {
	case tokLT:
		return Bool(c < 0), nil
	case tokLE:
		return Bool(c <= 0), nil
	case tokGT:
		return Bool(c > 0), nil
	}
	return Bool(c >= 0), nil
}

func compareInts(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// member evaluates x in y. A list compares its elements by E3 and a set its members by E4; a
// map holds a string if it is one of its keys, and an inherited name like toString is not one
// (Pyric prototype-chain-keys). In null is a null value error; in anything else is the official
// "function not found" for in (Pyric in-membership-and-proto-keys).
func member(m *meter, x, y Value) (Value, error) {
	switch y.kind {
	case KindList, KindSet:
		rel := relList
		if y.kind == KindSet {
			rel = relSet
		}
		items := y.items()
		if err := m.work(int64(len(items))); err != nil {
			return Value{}, err
		}
		for _, e := range items {
			eq, err := equal(m, rel, e, x)
			if err != nil {
				return Value{}, err
			}
			if eq {
				return Bool(true), nil
			}
		}
		return Bool(false), nil
	case KindMap:
		if x.kind != KindString {
			return Value{}, errUnsupported(fmt.Sprintf("map.in(%s)", x.kind), []string{"map.in(string)"})
		}
		if err := m.work(scanWork(len(x.str()))); err != nil {
			return Value{}, err
		}
		_, ok := y.mapv().entries[x.str()]
		return Bool(ok), nil
	case KindNull:
		return Value{}, errNull()
	}
	return Value{}, errNotFound("in")
}

// add evaluates x + y.
func add(m *meter, x, y Value) (Value, error) {
	switch {
	case x.kind == KindInt && y.kind == KindInt:
		s, ok := addInts(x.integer(), y.integer())
		if !ok {
			return Value{}, errOverflow()
		}
		return Int(s), nil
	case x.isNumber() && y.isNumber():
		return Float(x.number() + y.number()), nil
	case x.kind == KindString && y.kind == KindString:
		a, b := x.str(), y.str()
		// A high surrogate ending a and a low one starting b join into their pair (wtf8.go).
		size := len(a) + len(b)
		if joins(a, b) {
			size -= 2
		}
		if err := chargeString(m, size); err != nil {
			return Value{}, err
		}
		return String(joinWTF8(a, b)), nil
	case x.kind == KindDuration && y.kind == KindDuration:
		return addDurations(x.dur(), y.dur(), 1)
	case x.kind == KindTimestamp && y.kind == KindDuration:
		return shiftTimestamp(x.instant(), y.dur(), 1)
	case x.kind == KindDuration && y.kind == KindTimestamp:
		return shiftTimestamp(y.instant(), x.dur(), 1)
	}
	return Value{}, unsupportedOp(tokPlus, x, y,
		[2]string{"int", "int"}, [2]string{"float", "float"}, [2]string{"string", "string"},
		[2]string{"duration", "duration"}, [2]string{"duration", "timestamp"},
		[2]string{"timestamp", "duration"})
}

// subtract evaluates x - y.
func subtract(x, y Value) (Value, error) {
	switch {
	case x.kind == KindInt && y.kind == KindInt:
		s, ok := subInts(x.integer(), y.integer())
		if !ok {
			return Value{}, errOverflow()
		}
		return Int(s), nil
	case x.isNumber() && y.isNumber():
		return Float(x.number() - y.number()), nil
	case x.kind == KindDuration && y.kind == KindDuration:
		return addDurations(x.dur(), y.dur(), -1)
	case x.kind == KindTimestamp && y.kind == KindDuration:
		return shiftTimestamp(x.instant(), y.dur(), -1)
	case x.kind == KindTimestamp && y.kind == KindTimestamp:
		return timestampsApart(x.instant(), y.instant())
	}
	return Value{}, unsupportedOp(tokMinus, x, y,
		[2]string{"int", "int"}, [2]string{"float", "float"}, [2]string{"duration", "duration"},
		[2]string{"timestamp", "duration"}, [2]string{"timestamp", "timestamp"})
}

// multiply evaluates x * y, x / y and x % y. Ints divide toward zero and the remainder takes
// the dividend's sign (-7 / 2 == -3, -7 % 2 == -1: Pyric int-float-and-division and
// float-modulo-unary-minus). Overflow is an error on + - *, but the smallest int divided by -1
// is itself, as in Java and in Go (production, Rules Test API, S0-A; hidane, the emulator).
func multiply(op token, x, y Value) (Value, error) {
	if x.kind == KindInt && y.kind == KindInt {
		a, b := x.integer(), y.integer()
		switch {
		case op == tokStar:
			p, ok := mulInts(a, b)
			if !ok {
				return Value{}, errOverflow()
			}
			return Int(p), nil
		case b == 0:
			return Value{}, errDivideByZero()
		case op == tokSlash:
			return Int(a / b), nil
		}
		return Int(a % b), nil
	}
	if x.isNumber() && y.isNumber() {
		a, b := x.number(), y.number()
		switch op {
		case tokStar:
			return Float(a * b), nil
		case tokSlash:
			return Float(a / b), nil
		}
		return Float(math.Mod(a, b)), nil
	}
	return Value{}, unsupportedOp(op, x, y, [2]string{"int", "int"}, [2]string{"float", "float"})
}

// unary applies op x, op being tokNot or tokMinus.
func unary(m *meter, op token, x Value) (Value, error) {
	if err := reserved(x); err != nil {
		return Value{}, err
	}
	switch op {
	case tokNot:
		if x.kind != KindBool {
			return Value{}, errUnsupported("!"+x.kind.String(), []string{"!bool"})
		}
		return Bool(!x.boolean()), nil
	case tokMinus:
		switch x.kind {
		case KindInt:
			if x.integer() == math.MinInt64 {
				return Value{}, errOverflow()
			}
			return Int(-x.integer()), nil
		case KindFloat:
			return Float(-x.float()), nil
		case KindDuration:
			d := x.dur()
			return Value{kind: KindDuration, x: duration{-d.seconds, -d.nanos}}, nil
		}
		return Value{}, errUnsupported("-"+x.kind.String(), []string{"-int", "-float", "-duration"})
	}
	return Value{}, errorf("%s is not a unary operator", op)
}

func addInts(a, b int64) (int64, bool) {
	s := a + b
	return s, (s > a) == (b > 0)
}

func subInts(a, b int64) (int64, bool) {
	s := a - b
	return s, (s < a) == (b > 0)
}

func mulInts(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	neg := (a < 0) != (b < 0)
	hi, lo := bits.Mul64(absUint(a), absUint(b))
	if hi != 0 {
		return 0, false
	}
	if neg {
		if lo > 1<<63 {
			return 0, false
		}
		return -int64(lo), true // -(1<<63) wraps to MinInt64, which is right
	}
	if lo > math.MaxInt64 {
		return 0, false
	}
	return int64(lo), true
}

func absUint(a int64) uint64 {
	if a < 0 {
		return uint64(-a) // MinInt64 wraps to 1<<63, which is its magnitude
	}
	return uint64(a)
}

// selectField returns x.name: a map's entry. A missing entry is an error, as is an inherited
// name like constructor (Pyric prototype-chain-keys, undefined-field-access); a path's fields
// are the variables bound into it, and the paths the rules hold bind none (hidane).
func selectField(m *meter, x Value, name string) (Value, error) {
	if mv, ok := x.partial(); ok {
		return partialField(mv, name)
	}
	if err := reserved(x); err != nil {
		return Value{}, err
	}
	switch x.kind {
	case KindMap:
		return entry(x, name)
	case KindNull:
		return Value{}, errNull()
	case KindPath:
		return Value{}, errProperty(name)
	}
	return Value{}, errType(x.kind.String(), "map,path")
}

// entry returns a map's entry: an error when it is missing, and an error without a message
// when it holds a reserved value other than a PartialMap (resource.data in a list).
func entry(x Value, name string) (Value, error) {
	v, ok := x.mapv().entries[name]
	if !ok {
		return Value{}, errProperty(name)
	}
	if _, ok := v.partial(); ok {
		return v, nil
	}
	if err := reserved(v); err != nil {
		return Value{}, err
	}
	return v, nil
}

// selectIndex returns x[i]: a list's element, a map's entry, a string's character or a path's
// segment, string indexes counting UTF-16 code units. A negative index or one past the end is
// an error, never null (Pyric undefined-field-access).
func selectIndex(m *meter, x, i Value) (Value, error) {
	if mv, ok := x.partial(); ok && i.kind == KindString {
		return partialField(mv, i.str())
	}
	if err := reserved(x); err != nil {
		return Value{}, err
	}
	if err := reserved(i); err != nil {
		return Value{}, err
	}
	unsupported := func(expected ...string) *evalError {
		forms := make([]string, len(expected))
		for n, t := range expected {
			forms[n] = fmt.Sprintf("%s[%s]", x.kind, t)
		}
		return errUnsupported(fmt.Sprintf("%s[%s]", x.kind, i.kind), forms)
	}
	switch x.kind {
	case KindList:
		if i.kind != KindInt {
			return Value{}, unsupported("int")
		}
		items := x.items()
		n, err := checkIndex(i.integer(), len(items))
		if err != nil {
			return Value{}, err
		}
		return items[n], reserved(items[n])
	case KindMap:
		if i.kind != KindString {
			return Value{}, unsupported("string")
		}
		if err := m.work(scanWork(len(i.str()))); err != nil {
			return Value{}, err
		}
		return entry(x, i.str())
	case KindString:
		if i.kind != KindInt {
			return Value{}, unsupported("int")
		}
		return stringIndex(m, x.str(), i.integer())
	case KindPath:
		switch i.kind {
		case KindInt:
			segs := x.segments()
			n, err := checkIndex(i.integer(), len(segs))
			if err != nil {
				return Value{}, err
			}
			return String(segs[n]), nil
		case KindString:
			return Value{}, errProperty(i.str())
		}
		return Value{}, unsupported("string", "int")
	case KindNull:
		return Value{}, errNull()
	}
	return Value{}, errNotFound("[]")
}

// checkIndex checks an index into size elements.
func checkIndex(i int64, size int) (int, error) {
	if i < 0 || i >= int64(size) {
		return 0, errIndex(i, size)
	}
	return int(i), nil
}

// selectSlice returns x[lo:hi] of a list, a string or a path; a nil bound, or a null one, was
// left out. The start must be an index of x, and so must the one before the end: a slice never
// clamps, [0:0] is an error and [i:i] is empty only for 0 < i < size (Pyric
// range-slice-list-and-string, list-map-literals-and-slice; hidane's rule, the same).
func selectSlice(m *meter, x Value, lo, hi *Value) (Value, error) {
	if err := reserved(x); err != nil {
		return Value{}, err
	}
	var size int
	switch x.kind {
	case KindList:
		size = len(x.items())
	case KindString:
		n, err := stringUnits(m, x.str())
		if err != nil {
			return Value{}, err
		}
		size = n
	case KindPath:
		size = len(x.segments())
	case KindNull:
		return Value{}, errNull()
	default:
		return Value{}, errNotFound("@range")
	}
	bound := func(b *Value) (int64, bool, error) {
		if b == nil || b.kind == KindNull {
			return 0, false, nil
		}
		if err := reserved(*b); err != nil {
			return 0, false, err
		}
		if b.kind != KindInt {
			return 0, false, errUnsupported(fmt.Sprintf("%s[%s:%s]", x.kind, kindOrNull(lo), kindOrNull(hi)),
				[]string{
					fmt.Sprintf("%s[null:null]", x.kind), fmt.Sprintf("%s[null:int]", x.kind),
					fmt.Sprintf("%s[int:null]", x.kind), fmt.Sprintf("%s[int:int]", x.kind),
				})
		}
		return b.integer(), true, nil
	}
	from, hasFrom, err := bound(lo)
	if err != nil {
		return Value{}, err
	}
	to, hasTo, err := bound(hi)
	if err != nil {
		return Value{}, err
	}
	if hasFrom && (from < 0 || from >= int64(size)) {
		return Value{}, errIndex(from, size)
	}
	if hasTo && (to < 1 || to > int64(size)) {
		return Value{}, errIndex(to-1, size)
	}
	if !hasFrom {
		from = 0
	}
	if !hasTo {
		to = int64(size)
	}
	if from > to {
		return Value{}, errRange(from, to)
	}
	switch x.kind {
	case KindList:
		part := x.items()[from:to]
		if err := chargeList(m, len(part)); err != nil {
			return Value{}, err
		}
		return List(append([]Value{}, part...)), nil
	case KindPath:
		part := x.segments()[from:to]
		if err := chargeList(m, len(part)); err != nil {
			return Value{}, err
		}
		return pathOf(append([]string{}, part...)), nil
	}
	return sliceString(m, x.str(), int(from), int(to))
}

func kindOrNull(b *Value) Kind {
	if b == nil {
		return KindNull
	}
	return b.kind
}

// isTypes are the names "is" takes (hidane parser.rs IS_TYPES, from the emulator's grammar).
// A set is never "set" and nothing is a "vector": hidane measured both on the emulator, and
// production is not recorded (Pyric checks only that keys() is a list, not a set).
var isTypes = map[string]bool{
	"bool": true, "bytes": true, "constraint": true, "duration": true, "float": true,
	"int": true, "latlng": true, "list": true, "vector": true, "set": true, "number": true,
	"map": true, "string": true, "timestamp": true, "path": true, "map_diff": true,
}

// typeIs evaluates x is typ, typ being the type's name as written. An unknown name is an
// *evalError.
func typeIs(x Value, typ string) (Value, error) {
	if err := reserved(x); err != nil {
		return Value{}, err
	}
	if !isTypes[typ] {
		return Value{}, errorf("Unknown type name: %s.", clip(typ))
	}
	switch typ {
	case "number":
		return Bool(x.isNumber()), nil
	case "set", "vector":
		return Bool(false), nil
	}
	return Bool(x.kind.String() == typ), nil
}
