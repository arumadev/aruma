package rules

import (
	"math"
	"testing"
	"time"
)

// The operators: arithmetic, ordering, in, !, unary minus, x.f, x[i] and x[i:j].

const plusExpected = "Expected: int + int, float + float, string + string, duration + duration, duration + timestamp, timestamp + duration."

func TestArithmetic(t *testing.T) {
	ts := vts("2025-06-15T13:45:30.25Z")
	runValueCases(t, []valueCase{
		// Pyric int-float-and-division, float-modulo-unary-minus, arithmetic-operand-types.
		{"10 / 4 == 2", vOp(vi(10), tokSlash, vi(4)), vi(2), ""},
		{"-7 / 2 == -3", vOp(vi(-7), tokSlash, vi(2)), vi(-3), ""},
		{"10.0 / 4.0", vOp(vf(10), tokSlash, vf(4)), vf(2.5), ""},
		{"10 / 4.0 promotes", vOp(vi(10), tokSlash, vf(4)), vf(2.5), ""},
		{"10 % 3", vOp(vi(10), tokPercent, vi(3)), vi(1), ""},
		{"-7 % 2 takes the dividend's sign", vOp(vi(-7), tokPercent, vi(2)), vi(-1), ""},
		{"10 / 0 is an error, not Infinity", vOp(vi(10), tokSlash, vi(0)), Value{}, "Divide by zero error."},
		{"10 % 0", vOp(vi(10), tokPercent, vi(0)), Value{}, "Divide by zero error."},
		{"float division by zero", vOp(vf(1), tokSlash, vi(0)), vf(math.Inf(1)), ""},
		{"float modulo", vOp(vf(7.5), tokPercent, vi(2)), vf(1.5), ""},
		{"int + float", vOp(vi(3), tokPlus, vf(1.5)), vf(4.5), ""},
		{"1.5 + 1", vOp(vf(1.5), tokPlus, vi(1)), vf(2.5), ""},
		{"int + int stays int", vOp(vi(2), tokPlus, vi(2)), vi(4), ""},
		{"int * float", vOp(vi(2), tokStar, vf(1.25)), vf(2.5), ""},
		{"int - float", vOp(vi(2), tokMinus, vf(0.5)), vf(1.5), ""},
		// production (Rules Test API, S0-A): int overflow is an error on + - *, but
		// INT_MIN / -1 silently returns INT_MIN.
		{"MaxInt64 + 1 overflows", vOp(vi(math.MaxInt64), tokPlus, vi(1)), Value{}, "Integer value is out of the [-9223372036854775808, 9223372036854775807] range."},
		{"MinInt64 - 1 overflows", vOp(vi(math.MinInt64), tokMinus, vi(1)), Value{}, "out of the"},
		{"MaxInt64 * 2 overflows", vOp(vi(math.MaxInt64), tokStar, vi(2)), Value{}, "out of the"},
		{"MinInt64 * -1 overflows", vOp(vi(math.MinInt64), tokStar, vi(-1)), Value{}, "out of the"},
		{"MinInt64 * 1", vOp(vi(math.MinInt64), tokStar, vi(1)), vi(math.MinInt64), ""},
		{"-2^62 * 2", vOp(vi(-1<<62), tokStar, vi(2)), vi(math.MinInt64), ""},
		{"MinInt64 / -1 is MinInt64", vOp(vi(math.MinInt64), tokSlash, vi(-1)), vi(math.MinInt64), ""},
		{"MinInt64 % -1", vOp(vi(math.MinInt64), tokPercent, vi(-1)), vi(0), ""},
		{"MaxInt64 + MinInt64", vOp(vi(math.MaxInt64), tokPlus, vi(math.MinInt64)), vi(-1), ""},
		{"string + string", vOp(vs("users/"), tokPlus, vs("alice")), vs("users/alice"), ""},
		// Pyric cross-type-operator-overloads: + takes no list, bytes or map.
		{"[1] + [2] is an error", vOp(vl(vi(1)), tokPlus, vl(vi(2))), Value{}, "Received: list + list. " + plusExpected},
		{"b'abc' + b'd'", vOp(vbytes("abc"), tokPlus, vbytes("d")), Value{}, "Received: bytes + bytes."},
		{"map + map", vOp(vm(), tokPlus, vm()), Value{}, "Received: map + map."},
		{"'a' + 1", vOp(vs("a"), tokPlus, vi(1)), Value{}, "Received: string + int."},
		{"1 + 'a'", vOp(vi(1), tokPlus, vs("a")), Value{}, "Received: int + string."},
		{"string + list", vOp(vs("a"), tokPlus, vl()), Value{}, "Received: string + list."},
		{"string + timestamp", vOp(vs("at "), tokPlus, ts), Value{}, "Received: string + timestamp."},
		{"string + duration", vOp(vs("for "), tokPlus, vdur(time.Minute)), Value{}, "Received: string + duration."},
		{"timestamp + timestamp", vOp(ts, tokPlus, ts), Value{}, "Received: timestamp + timestamp."},
		{"duration + int", vOp(vdur(time.Minute), tokPlus, vi(1)), Value{}, "Received: duration + int."},
		{"string - string", vOp(vs("a"), tokMinus, vs("a")), Value{}, "Received: string - string. Expected: int - int, float - float, duration - duration, timestamp - duration, timestamp - timestamp."},
		{"string * int", vOp(vs("a"), tokStar, vi(2)), Value{}, "Received: string * int. Expected: int * int, float * float."},
		{"string / string", vOp(vs("a"), tokSlash, vs("a")), Value{}, "Received: string / string."},
		{"string % string", vOp(vs("a"), tokPercent, vs("a")), Value{}, "Received: string % string."},
		// Pyric cross-type-operator-overloads: timestamps and durations.
		{"timestamp + duration", vOp(ts, tokPlus, vdur(time.Hour)), vts("2025-06-15T14:45:30.25Z"), ""},
		{"duration + timestamp", vOp(vdur(time.Hour), tokPlus, ts), vts("2025-06-15T14:45:30.25Z"), ""},
		{"timestamp - duration", vOp(ts, tokMinus, vdur(500*time.Millisecond)), vts("2025-06-15T13:45:29.75Z"), ""},
		{"timestamp - timestamp", vOp(ts, tokMinus, vts("2025-06-15T12:00:00Z")), vdur(time.Hour + 45*time.Minute + 30250*time.Millisecond), ""},
		{"timestamp - later timestamp", vOp(vts("2025-06-15T12:00:00Z"), tokMinus, ts), vdur(-(time.Hour + 45*time.Minute + 30250*time.Millisecond)), ""},
		{"duration + duration", vOp(vdur(time.Hour), tokPlus, vdur(30*time.Minute)), vdur(90 * time.Minute), ""},
		{"duration - duration", vOp(vdur(time.Second), tokMinus, vdur(1500*time.Millisecond)), vdur(-500 * time.Millisecond), ""},
		{"timestamp past year 9999", vOp(vts("9999-12-31T23:59:59Z"), tokPlus, vdur(time.Second)), Value{}, "Timestamp is not valid."},
		{"timestamp before year 1", vOp(vts("0001-01-01T00:00:00Z"), tokMinus, vdur(time.Nanosecond)), Value{}, "Timestamp is not valid."},
		{"duration past 10,000 years", vOpOn(vFn("duration.value", vi(maxDurationSeconds), vs("s")), tokPlus, vLit(vdur(time.Second))), Value{}, "Duration is not valid."},
	})
}

func TestUnary(t *testing.T) {
	runValueCases(t, []valueCase{
		{"!true", vUn(tokNot, vb(true)), vb(false), ""},
		{"!'a'", vUn(tokNot, vs("a")), Value{}, "Unsupported operation error. Received: !string. Expected: !bool."},
		{"-int", vUn(tokMinus, vi(3)), vi(-3), ""},
		{"-float", vUn(tokMinus, vf(0)), vf(negZero), ""},
		{"-duration", vUn(tokMinus, vdur(time.Second)), vdur(-time.Second), ""},
		// Pyric simulator-language-semantics FS-SIM-2b.
		{"-string", vUn(tokMinus, vs("a")), Value{}, "Received: -string. Expected: -int, -float, -duration."},
		{"-MinInt64 overflows", vUn(tokMinus, vi(math.MinInt64)), Value{}, "out of the"},
	})
}

const ordering = "Expected: string < string, bytes < bytes, int < int, float < float, duration < duration, timestamp < timestamp, constraint < any, any < constraint."

func TestOrdering(t *testing.T) {
	ts := vts("2025-06-15T13:45:30Z")
	runValueCases(t, []valueCase{
		// Pyric ordering-operand-types and int-float-and-division.
		{"int < float", vOp(vi(3), tokLT, vf(3.5)), vb(true), ""},
		{"float <= int", vOp(vf(2.5), tokLE, vi(3)), vb(true), ""},
		{"int >= int", vOp(vi(3), tokGE, vi(3)), vb(true), ""},
		{"float 9.5 > int 10 is false", vOp(vf(9.5), tokGT, vi(10)), vb(false), ""},
		{"NaN < 1 is false", vOp(vf(math.NaN()), tokLT, vi(1)), vb(false), ""},
		{"NaN >= NaN is false", vOp(vf(math.NaN()), tokGE, vf(math.NaN())), vb(false), ""},
		{"string < string", vOp(vs("a.txt"), tokLT, vs("b")), vb(true), ""},
		{"bytes order", vOp(vbytes("\x00"), tokLT, vbytes("\xff")), vb(true), ""},
		{"b'abc' > b'ab'", vOp(vbytes("abc"), tokGT, vbytes("ab")), vb(true), ""},
		{"durations", vOp(vdur(time.Minute), tokGT, vdur(59*time.Second)), vb(true), ""},
		{"negative durations", vOp(vdur(-1500*time.Millisecond), tokLT, vdur(-time.Second)), vb(true), ""},
		{"timestamps", vOp(ts, tokLT, vts("2025-06-15T13:45:31Z")), vb(true), ""},
		// Strings order by UTF-16 code unit, as Java's compareTo (hidane): U+FF61 is a single
		// unit 0xFF61, 😀 starts with the surrogate 0xD83D, so 😀 < U+FF61, unlike byte order.
		{"UTF-16 order", vOp(vs("😀"), tokLT, vs("｡")), vb(true), ""},
		{"prefix first", vOp(vs("ab"), tokLT, vs("abc")), vb(true), ""},
		{"int < string", vOp(vi(1), tokLT, vs("a")), Value{}, "Received: int < string. " + ordering},
		{"string >= int", vOp(vs("a"), tokGE, vi(2)), Value{}, "Received: string >= int."},
		{"bool < bool", vOp(vb(false), tokLT, vb(true)), Value{}, "Received: bool < bool."},
		{"timestamp > int", vOp(ts, tokGT, vi(1)), Value{}, "Received: timestamp > int."},
		{"list < list", vOp(vl(), tokLT, vl()), Value{}, "Received: list < list."},
		{"map < map", vOp(vm(), tokLT, vm()), Value{}, "Received: map < map."},
		{"int < null", vOp(vi(1), tokLT, vn()), Value{}, "Received: int < null."},
	})
}

func TestIn(t *testing.T) {
	runValueCases(t, []valueCase{
		// Pyric in-membership-and-proto-keys, prototype-chain-keys.
		{"in a list", vOp(vs("image/png"), tokIn, vl(vs("image/png"), vs("image/jpeg"))), vb(true), ""},
		{"not in a list", vOp(vs("text/plain"), tokIn, vl(vs("image/png"))), vb(false), ""},
		{"a key of a map", vOp(vs("owner"), tokIn, vm("owner", vs("alice"))), vb(true), ""},
		{"an inherited name is no key", vOp(vs("toString"), tokIn, vm("owner", vs("alice"))), vb(false), ""},
		{"a key named toString is a key", vOp(vs("toString"), tokIn, vm("toString", vi(1))), vb(true), ""},
		{"in a set", vOp(vs("a"), tokIn, vset(vs("a"), vs("b"))), vb(true), ""},
		{"not in a set", vOp(vs("items"), tokIn, vset(vs("a"), vs("b"))), vb(false), ""},
		{"in null", vOp(vs("a"), tokIn, vn()), Value{}, "Null value error."},
		{"in a string", vOp(vs("a"), tokIn, vs("abc")), Value{}, "Function not found error: Name: [in]."},
		{"in an int", vOp(vs("a"), tokIn, vi(5)), Value{}, "Name: [in]."},
		{"in a bool", vOp(vs("a"), tokIn, vb(true)), Value{}, "Name: [in]."},
		{"in a timestamp", vOp(vs("a"), tokIn, vts("2025-01-01T00:00:00Z")), Value{}, "Name: [in]."},
		{"in a path", vOp(vs("a"), tokIn, vp("a")), Value{}, "Name: [in]."},
		{"in a map diff", vOpOn(vLit(vs("a")), tokIn, vCall(vm(), "diff", vm())), Value{}, "Name: [in]."},
		{"an int in a map", vOp(vi(1), tokIn, vm("a", vi(1))), Value{}, "Unsupported operation error. Received: map.in(int). Expected: map.in(string)."},
	})
}

func TestSelectField(t *testing.T) {
	data := vm("name", vs("alice"), "l", vl(vi(1), vi(2)), "nul", vn())
	field := func(x Value, f string) testExpr {
		return func(m *meter) (Value, error) { return selectField(m, x, f) }
	}
	runValueCases(t, []valueCase{
		// Pyric undefined-field-access, metadata-access, prototype-chain-keys.
		{"a field", field(data, "name"), vs("alice"), ""},
		{"a null field", field(data, "nul"), vn(), ""},
		{"a missing field is an error, not null", field(data, "missing"), Value{}, "Property missing is undefined on object."},
		{"an inherited name", field(data, "constructor"), Value{}, "Property constructor is undefined on object."},
		{"size without a call on a map", field(vm("a", vi(1)), "size"), Value{}, "Property size is undefined on object."},
		{"length on a list", field(vl(vi(1)), "length"), Value{}, "Type error. Received: [list] Expected: [map,path]."},
		{"length on a string", field(vs("abc"), "length"), Value{}, "Type error. Received: [string] Expected: [map,path]."},
		{"a field of null", field(vn(), "a"), Value{}, "Null value error."},
		{"a field of a path", field(vp("databases"), "segments"), Value{}, "Property segments is undefined on object."},
	})
}

func TestSelectIndex(t *testing.T) {
	data := vm("name", vs("alice"), "m", vm("a", vi(1)))
	index := func(x, i Value) testExpr {
		return func(m *meter) (Value, error) { return selectIndex(m, x, i) }
	}
	list := vl(vi(1), vi(2))
	runValueCases(t, []valueCase{
		// Pyric undefined-field-access, list-map-literals-and-slice, bytes-literal.
		{"a map key", index(data, vs("name")), vs("alice"), ""},
		{"a missing key", index(data, vs("missing")), Value{}, "Property missing is undefined on object."},
		{"an int key on a map", index(data, vi(0)), Value{}, "Unsupported operation error. Received: map[int]. Expected: map[string]."},
		{"a list index", index(list, vi(1)), vi(2), ""},
		{"past the end", index(list, vi(5)), Value{}, "Index out of bound error. Index: [5] , size: [2]."},
		{"negative", index(list, vi(-1)), Value{}, "Index out of bound error. Index: [-1] , size: [2]."},
		{"a string key on a list", index(list, vs("length")), Value{}, "Unsupported operation error. Received: list[string]. Expected: list[int]."},
		{"'abc'[0]", index(vs("abc"), vi(0)), vs("a"), ""},
		{"a string key on a string", index(vs("abc"), vs("length")), Value{}, "Received: string[string]. Expected: string[int]."},
		// UTF-16 code units: 😀 is two, and either half alone is a lone surrogate, not U+FFFD
		// (the review's probes of the Rules Test API: '😀'[0] == '\ufffd' is denied). hidane
		// (D022, emulator) gives U+FFFD; production wins.
		{"after a surrogate pair", index(vs("😀a"), vi(2)), vs("a"), ""},
		{"the high half", index(vs("😀a"), vi(0)), vs(lone(0xD83D)), ""},
		{"the low half", index(vs("😀a"), vi(1)), vs(lone(0xDE00)), ""},
		{"Arabic", index(vs("نص"), vi(1)), vs("ص"), ""},
		{"a path segment", index(vp("databases", "(default)"), vi(0)), vs("databases"), ""},
		{"a path key", index(vp("databases"), vs("segments")), Value{}, "Property segments is undefined on object."},
		{"a path past the end", index(vp("a", "b"), vi(2)), Value{}, "Index out of bound error. Index: [2] , size: [2]."},
		{"bytes", index(vbytes("abc"), vi(0)), Value{}, "Function not found error: Name: [[]]."},
		{"a timestamp", index(vts("2025-01-01T00:00:00Z"), vi(0)), Value{}, "Name: [[]]."},
		{"a latlng", index(LatLng(1, 2), vi(0)), Value{}, "Name: [[]]."},
		{"a duration", index(vdur(time.Hour), vi(0)), Value{}, "Name: [[]]."},
		{"null", index(vn(), vi(0)), Value{}, "Null value error."},
	})
}

func TestSelectSlice(t *testing.T) {
	slice := func(x Value, lo, hi *Value) testExpr {
		return func(m *meter) (Value, error) { return selectSlice(m, x, lo, hi) }
	}
	i := func(n int64) *Value { v := Int(n); return &v }
	list := vl(vs("a"), vs("b"), vs("c"), vs("d"))
	runValueCases(t, []valueCase{
		// Pyric range-slice-list-and-string and list-map-literals-and-slice: a slice never
		// clamps; the start must be an index and so must the one before the end.
		{"mid-slice", slice(list, i(1), i(3)), vl(vs("b"), vs("c")), ""},
		{"full length", slice(list, i(0), i(4)), list, ""},
		{"[i:i] is empty", slice(list, i(2), i(2)), vl(), ""},
		{"[n-1:n-1] is empty", slice(list, i(3), i(3)), vl(), ""},
		{"[n-1:n] is the last", slice(list, i(3), i(4)), vl(vs("d")), ""},
		{"end past the length", slice(list, i(0), i(9)), Value{}, "Index out of bound error. Index: [8] , size: [4]."},
		{"[0:0]", slice(list, i(0), i(0)), Value{}, "Index out of bound error. Index: [-1] , size: [4]."},
		{"[n:n]", slice(list, i(4), i(4)), Value{}, "Index out of bound error. Index: [4] , size: [4]."},
		{"[1:0]", slice(list, i(1), i(0)), Value{}, "Index: [-1]"},
		{"start after end", slice(list, i(3), i(1)), Value{}, "Illegal range error. From index: [3] , To index: [1]."},
		{"empty [0:0]", slice(vl(), i(0), i(0)), Value{}, "Index out of bound error. Index: [0] , size: [0]."},
		{"no bounds", slice(list, nil, nil), list, ""},
		{"null bounds", slice(list, &Value{kind: KindNull}, i(1)), vl(vs("a")), ""},
		{"a string bound", slice(list, func() *Value { v := String("a"); return &v }(), nil), Value{}, "Unsupported operation error. Received: list[string:null]."},
		{"substring", slice(vs("abcdef"), i(1), i(4)), vs("bcd"), ""},
		{"string [1:1]", slice(vs("abc"), i(1), i(1)), vs(""), ""},
		{"string [0:0]", slice(vs("abc"), i(0), i(0)), Value{}, "Index: [-1] , size: [3]."},
		{"string [n:n]", slice(vs("abcde"), i(5), i(5)), Value{}, "Index out of bound error. Index: [5] , size: [5]."},
		{"string start after end", slice(vs("abc"), i(2), i(1)), Value{}, "Illegal range error. From index: [2] , To index: [1]."},
		{"UTF-16 units", slice(vs("a😀b"), i(1), i(3)), vs("😀"), ""},
		{"a path", slice(vp("databases", "(default)", "documents"), i(1), nil), vp("(default)", "documents"), ""},
		{"bytes", slice(vbytes("ab"), i(0), i(1)), Value{}, "Function not found error: Name: [@range]."},
		{"null", slice(vn(), nil, nil), Value{}, "Null value error."},
	})
}
