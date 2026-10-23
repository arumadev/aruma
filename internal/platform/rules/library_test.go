package rules

import (
	"math"
	"strings"
	"testing"
	"time"
)

// The rules.* reference, function by function and method by method.

func TestStringMethods(t *testing.T) {
	runValueCases(t, []valueCase{
		// size counts UTF-16 code units: production (Rules Test API, S0-A) and Pyric
		// string-literals-and-regex ('\ud83d\ude00'.size() == 2, '\u00e9'.size() == 1).
		{"size", vCall(vs("abc"), "size"), vi(3), ""},
		{"size of é", vCall(vs("é"), "size"), vi(1), ""},
		{"size of 😀", vCall(vs("😀"), "size"), vi(2), ""},
		{"size of Arabic and emoji", vCall(vs("نص 🙂"), "size"), vi(5), ""},
		{"size with an argument", vCall(vs("a"), "size", vi(1)), Value{}, "Incorrect number of arguments. Received: 1. Expected: string.size()."},
		// production (Rules Test API, S0-A): lower() and upper() change ASCII letters only.
		{"lower", vCall(vs("AbC"), "lower"), vs("abc"), ""},
		{"upper", vCall(vs("AbC"), "upper"), vs("ABC"), ""},
		{"lower leaves non-ASCII", vCall(vs("ÉA"), "lower"), vs("Éa"), ""},
		{"upper leaves non-ASCII", vCall(vs("éa"), "upper"), vs("éA"), ""},
		{"lower(1)", vCall(vs("a"), "lower", vi(1)), Value{}, "Incorrect number of arguments. Received: 1. Expected: string.lower()."},
		// production (Rules Test API, S0-A): trim() removes only characters up to U+0020.
		{"trim", vCall(vs("  x y  "), "trim"), vs("x y"), ""},
		{"trim control characters", vCall(vs("\t\n\x00x\r"), "trim"), vs("x"), ""},
		{"trim leaves U+00A0", vCall(vs("\u00a0x\u00a0"), "trim"), vs("\u00a0x\u00a0"), ""},
		// Pyric bytes-toutf8-and-hashing.
		{"toUtf8", vCall(vs("é"), "toUtf8"), vbytes("\xc3\xa9"), ""},
		{"toUtf8 size counts bytes", vThen(vCall(vs("日本"), "toUtf8"), "size"), vi(6), ""},
		{"matches", vCall(vs("hello"), "matches", vs("hello")), vb(true), ""},
		{"split", vCall(vs("a-b-c"), "split", vs("-")), vl(vs("a"), vs("b"), vs("c")), ""},
		{"replace", vCall(vs("aXa"), "replace", vs("a"), vs("b")), vs("bXb"), ""},
		{"matches(1)", vCall(vs("a"), "matches", vi(1)), Value{}, "Unsupported operation error. Received: string.matches(int). Expected: string.matches(string)."},
		{"replace with one argument", vCall(vs("a"), "replace", vs("a")), Value{}, "Incorrect number of arguments. Received: 1. Expected: string.replace(string, string)."},
		{"no such method", vCall(vs("a"), "keys"), Value{}, "Function not found error: Name: [keys]."},
		{"int receiver has no lower()", vCall(vi(10), "lower"), Value{}, "Function not found error: Name: [lower]."},
	})
}

func TestBytesMethods(t *testing.T) {
	runValueCases(t, []valueCase{
		{"size", vCall(vbytes("\x00\x01"), "size"), vi(2), ""},
		// production (Rules Test API, S0-A) and Pyric: toBase64 is URL-safe and padded.
		{"toBase64 padded", vCall(vbytes("hi"), "toBase64"), vs("aGk="), ""},
		{"toBase64 URL-safe", vCall(vbytes("~~~"), "toBase64"), vs("fn5-"), ""},
		{"toBase64 of \\xff", vCall(vbytes("\xff"), "toBase64"), vs("_w=="), ""},
		// Pyric: hex is upper case.
		{"toHexString", vCall(vbytes("\x00\x01\xff"), "toHexString"), vs("0001FF"), ""},
		{"toHexString of escapes", vCall(vbytes("\\'\"\n\r\t\b\f"), "toHexString"), vs("5C27220A0D09080C"), ""},
		{"size(1)", vCall(vbytes(""), "size", vi(1)), Value{}, "Expected: bytes.size()."},
		{"no lower()", vCall(vbytes("a"), "lower"), Value{}, "Name: [lower]."},
	})
}

func TestListMethods(t *testing.T) {
	keys := vl(vs("a"), vs("b"))
	runValueCases(t, []valueCase{
		// Pyric list-methods-concat-removeall-toset, stdlib-sets-and-mapdiff,
		// upload-primitives-boundaries.
		{"size", vCall(keys, "size"), vi(2), ""},
		{"concat keeps order and duplicates", vCall(vl(vi(1), vi(2)), "concat", vl(vi(2), vi(3))), vl(vi(1), vi(2), vi(2), vi(3)), ""},
		{"concat of two empty lists", vCall(vl(), "concat", vl()), vl(), ""},
		{"concat a string", vCall(keys, "concat", vs("a")), Value{}, "Unsupported operation error. Received: list.concat(string). Expected: list.concat(list)."},
		{"concat a set", vCall(keys, "concat", vset()), Value{}, "Received: list.concat(set)."},
		{"concat nothing", vCall(keys, "concat"), Value{}, "Incorrect number of arguments. Received: 0. Expected: list.concat(list)."},
		{"removeAll every occurrence", vCall(vl(vs("a"), vs("b"), vs("a")), "removeAll", vl(vs("a"))), vl(vs("b")), ""},
		{"removeAll nothing", vCall(keys, "removeAll", vl()), keys, ""},
		{"removeAll a set", vCall(keys, "removeAll", vset()), Value{}, "Received: list.removeAll(set). Expected: list.removeAll(list)."},
		{"removeAll a string", vCall(keys, "removeAll", vs("a")), Value{}, "Received: list.removeAll(string)."},
		{"toSet dedupes", vThen(vCall(vl(vs("a"), vs("a"), vs("b")), "toSet"), "size"), vi(2), ""},
		{"toSet equals the set", vOpOn(vCall(vl(vs("b"), vs("a"), vs("b")), "toSet"), tokEQ, vLit(vset(vs("a"), vs("b")))), vb(true), ""},
		{"toSet(1)", vCall(keys, "toSet", vi(1)), Value{}, "Incorrect number of arguments. Received: 1. Expected: list.toSet()."},
		{"hasAll", vCall(keys, "hasAll", vl(vs("a"))), vb(true), ""},
		{"hasAll missing", vCall(keys, "hasAll", vl(vs("a"), vs("z"))), vb(false), ""},
		{"hasAll a set", vCall(keys, "hasAll", vset(vs("a"))), Value{}, "Unsupported operation error. Received: list.hasAll(set). Expected: list.hasAll(list)."},
		{"hasAll a map", vCall(keys, "hasAll", vm()), Value{}, "Received: list.hasAll(map)."},
		{"hasAll two lists", vCall(keys, "hasAll", vl(), vl()), Value{}, "Incorrect number of arguments. Received: 2. Expected: list.hasAll(list)."},
		{"hasAny", vCall(keys, "hasAny", vl(vs("z"), vs("b"))), vb(true), ""},
		{"hasAny of nothing is false", vCall(keys, "hasAny", vl()), vb(false), ""},
		{"hasAny a string", vCall(keys, "hasAny", vs("a")), Value{}, "Received: list.hasAny(string). Expected: list.hasAny(list)."},
		{"hasAny with no argument", vCall(keys, "hasAny"), Value{}, "Incorrect number of arguments. Received: 0. Expected: list.hasAny(list)."},
		{"hasOnly", vCall(vl(vs("a"), vs("a")), "hasOnly", vl(vs("a"), vs("b"))), vb(true), ""},
		{"hasOnly missing", vCall(keys, "hasOnly", vl(vs("a"))), vb(false), ""},
		{"an empty list hasOnly anything", vCall(vl(), "hasOnly", vl()), vb(true), ""},
		{"hasOnly null", vCall(keys, "hasOnly", vn()), Value{}, "Received: list.hasOnly(null). Expected: list.hasOnly(list)."},
		// join converts as string() does (Pyric listMethod join).
		{"join", vCall(keys, "join", vs(",")), vs("a,b"), ""},
		{"join with an empty separator", vCall(keys, "join", vs("")), vs("ab"), ""},
		{"join an empty list", vCall(vl(), "join", vs(",")), vs(""), ""},
		{"join null, int, bool, float, path", vCall(vl(vn(), vi(1), vb(true), vf(2), vf(0.5), vp("a", "b")), "join", vs(",")), vs("null,1,true,2.0,0.5,/a/b"), ""},
		{"join a list element", vCall(vl(vl()), "join", vs(",")), Value{}, "Received: string(list)."},
		{"join a map element", vCall(vl(vm()), "join", vs(",")), Value{}, "Received: string(map)."},
		{"join a bytes element", vCall(vl(vbytes("a")), "join", vs(",")), Value{}, "Received: string(bytes)."},
		{"join a duration element", vCall(vl(vdur(time.Second)), "join", vs(",")), Value{}, "Received: string(duration)."},
		{"join without a separator", vCall(keys, "join"), Value{}, "Incorrect number of arguments. Received: 0. Expected: list.join(string)."},
		{"join two separators", vCall(keys, "join", vs(","), vs(",")), Value{}, "Received: 2. Expected: list.join(string)."},
		{"join an int separator", vCall(keys, "join", vi(1)), Value{}, "Unsupported operation error. Received: list.join(int). Expected: list.join(string)."},
		// Pyric listMethod receiver: list methods on other receivers are not found.
		{"concat on a string", vCall(vs("a"), "concat", vl()), Value{}, "Function not found error: Name: [concat]."},
		{"join on a map", vCall(vm(), "join", vs(",")), Value{}, "Name: [join]."},
		{"toSet on a set", vCall(vset(), "toSet"), Value{}, "Name: [toSet]."},
		{"removeAll on a set", vCall(vset(), "removeAll", vl()), Value{}, "Name: [removeAll]."},
		{"concat on a set", vCall(vset(), "concat", vl()), Value{}, "Name: [concat]."},
		{"hasAll on a map", vCall(vm(), "hasAll", vl()), Value{}, "Name: [hasAll]."},
		{"keys on a list", vCall(keys, "keys"), Value{}, "Name: [keys]."},
		{"values on a list", vCall(keys, "values"), Value{}, "Name: [values]."},
		{"get on a list", vCall(keys, "get", vi(0), vn()), Value{}, "Name: [get]."},
		{"size of null", vCall(vn(), "size"), Value{}, "Null value error."},
		{"size of an int", vCall(vi(1), "size"), Value{}, "Name: [size]."},
	})
}

func TestSetMethods(t *testing.T) {
	s := vset(vs("a"), vs("b"), vs("c"))
	runValueCases(t, []valueCase{
		// Pyric set-algebra-difference-union-intersection, stdlib-sets-and-mapdiff.
		{"size", vCall(s, "size"), vi(3), ""},
		{"size(1)", vCall(s, "size", vi(1)), Value{}, "Incorrect number of arguments. Received: 1. Expected: set.size()."},
		{"hasAll a list", vCall(s, "hasAll", vl(vs("a"), vs("c"))), vb(true), ""},
		{"hasAll a missing element", vCall(s, "hasAll", vl(vs("z"))), vb(false), ""},
		{"hasAny a set", vCall(s, "hasAny", vset(vs("z"), vs("b"))), vb(true), ""},
		{"hasOnly a list", vCall(s, "hasOnly", vl(vs("a"), vs("b"), vs("c"), vs("d"))), vb(true), ""},
		{"hasOnly missing", vCall(s, "hasOnly", vl(vs("a"))), vb(false), ""},
		{"hasAny a string", vCall(s, "hasAny", vs("a")), Value{}, "Unsupported operation error. Received: set.hasAny(string). Expected: set.hasAny(set), set.hasAny(list)."},
		{"hasAll a map", vCall(s, "hasAll", vm()), Value{}, "Received: set.hasAll(map). Expected: set.hasAll(set), set.hasAll(list)."},
		{"hasOnly a string", vCall(s, "hasOnly", vs("a")), Value{}, "Received: set.hasOnly(string)."},
		{"hasAll two", vCall(s, "hasAll", vl(), vl()), Value{}, "Incorrect number of arguments. Received: 2. Expected: set.hasAll(set), set.hasAll(list)."},
		{"hasAny none", vCall(s, "hasAny"), Value{}, "Received: 0. Expected: set.hasAny(set), set.hasAny(list)."},
		{"difference", vCall(s, "difference", vset(vs("a"), vs("z"))), vset(vs("b"), vs("c")), ""},
		{"union", vCall(s, "union", vset(vs("c"), vs("d"))), vset(vs("a"), vs("b"), vs("c"), vs("d")), ""},
		{"intersection", vCall(s, "intersection", vset(vs("b"), vs("c"), vs("z"))), vset(vs("b"), vs("c")), ""},
		{"intersection with no overlap", vThen(vCall(s, "intersection", vset(vs("z"))), "size"), vi(0), ""},
		{"difference of a list", vCall(s, "difference", vl(vs("a"))), Value{}, "Unsupported operation error. Received: set.difference(list). Expected: set.difference(set)."},
		{"difference of a map", vCall(s, "difference", vm()), Value{}, "Received: set.difference(map)."},
		{"union of a string", vCall(s, "union", vs("a")), Value{}, "Received: set.union(string). Expected: set.union(set)."},
		{"union of nothing", vCall(s, "union"), Value{}, "Incorrect number of arguments. Received: 0. Expected: set.union(set)."},
		{"intersection of two", vCall(s, "intersection", vset(), vset()), Value{}, "Received: 2. Expected: set.intersection(set)."},
		{"difference on a list", vCall(vl(), "difference", vset()), Value{}, "Name: [difference]."},
		{"union on a list", vCall(vl(), "union", vset()), Value{}, "Name: [union]."},
		{"intersection on a string", vCall(vs("a"), "intersection", vset()), Value{}, "Name: [intersection]."},
		{"keys on a set", vCall(s, "keys"), Value{}, "Name: [keys]."},
		{"values on a set", vCall(s, "values"), Value{}, "Name: [values]."},
		{"get on a set", vCall(s, "get", vs("a"), vn()), Value{}, "Name: [get]."},
		{"diff on a set", vCall(s, "diff", vm()), Value{}, "Name: [diff]."},
	})
}

func TestMapMethods(t *testing.T) {
	data := vm("b", vi(1), "a", vi(2), "m", vm("x", vm("y", vs("deep"))), "s", vs("str"))
	runValueCases(t, []valueCase{
		{"size", vCall(data, "size"), vi(4), ""},
		// keys() sorted by code point, values() in the order written: Pyric keyOrder. hidane
		// sorts keys by UTF-16 code unit (emulator); Pyric (production) by code point, and wins.
		{"keys sorted", vCall(vm("b", vi(1), "a", vi(2)), "keys"), vl(vs("a"), vs("b")), ""},
		{"keys by code point", vCall(vm("_", vi(0), "a", vi(0), "A", vi(0), "1", vi(0)), "keys"), vl(vs("1"), vs("A"), vs("_"), vs("a")), ""},
		{"'10' before '9'", vCall(vm("9", vi(0), "10", vi(0)), "keys"), vl(vs("10"), vs("9")), ""},
		{"not by UTF-16 unit", vCall(vm("😀", vi(0), "\uff61", vi(0)), "keys"), vl(vs("\uff61"), vs("😀")), ""},
		{"a prefix first", vCall(vm("ab", vi(0), "a", vi(0)), "keys"), vl(vs("a"), vs("ab")), ""},
		{"values in written order", vCall(vm("b", vi(1), "a", vi(2), "10", vi(3), "9", vi(4)), "values"), vl(vi(1), vi(2), vi(3), vi(4)), ""},
		{"values of an empty map", vCall(vm(), "values"), vl(), ""},
		{"values(1)", vCall(data, "values", vi(1)), Value{}, "Incorrect number of arguments. Received: 1. Expected: map.values()."},
		{"keys(1)", vCall(data, "keys", vi(1)), Value{}, "Received: 1. Expected: map.keys()."},
		// get: Pyric map-get-string-and-list-form, upload-primitives-boundaries.
		{"get present", vCall(data, "get", vs("a"), vi(0)), vi(2), ""},
		{"get absent", vCall(data, "get", vs("z"), vs("default")), vs("default"), ""},
		{"get a path", vCall(data, "get", vl(vs("m"), vs("x"), vs("y")), vn()), vs("deep"), ""},
		{"get a path, leaf absent", vCall(data, "get", vl(vs("m"), vs("x"), vs("z")), vi(0)), vi(0), ""},
		{"get a path, middle absent", vCall(data, "get", vl(vs("m"), vs("q"), vs("y")), vi(0)), vi(0), ""},
		{"get a path, top absent", vCall(data, "get", vl(vs("q"), vs("y")), vi(0)), vi(0), ""},
		{"get a path through a string", vCall(data, "get", vl(vs("s"), vs("y")), vi(0)), vi(0), ""},
		{"get a path through an int", vCall(data, "get", vl(vs("a"), vs("y")), vi(0)), vi(0), ""},
		{"get an empty path", vCall(vm("a", vi(1)), "get", vl(), vn()), vm("a", vi(1)), ""},
		{"get with three arguments", vCall(data, "get", vs("a"), vn(), vn()), Value{}, "Incorrect number of arguments. Received: 3. Expected: map.get(string, any), map.get(list, any)."},
		{"get an int key", vCall(data, "get", vi(1), vn()), Value{}, "Unsupported operation error. Received: map.get(int, null)."},
		{"get a path of ints", vCall(data, "get", vl(vi(1)), vn()), Value{}, "Type error. Received: [int] Expected: [string]."},
		{"diff two arguments", vCall(data, "diff", vm(), vm()), Value{}, "Incorrect number of arguments. Received: 2. Expected: map.diff(map)."},
		{"diff a string", vCall(data, "diff", vs("a")), Value{}, "Unsupported operation error. Received: map.diff(string). Expected: map.diff(map)."},
	})
}

func TestMapDiffMethods(t *testing.T) {
	// request.resource.data.diff(resource.data): the receiver is after the write.
	after := vm("title", vs("new"), "owner", vs("alice"), "label", vs("x"))
	before := vm("title", vs("old"), "owner", vs("alice"), "gone", vi(1))
	diff := vCall(after, "diff", before)
	runValueCases(t, []valueCase{
		// Pyric required-fields-and-mapdiff, stdlib-sets-and-mapdiff, stdlib-storage-p2-native-advanced.
		{"addedKeys", vThen(diff, "addedKeys"), vset(vs("label")), ""},
		{"removedKeys", vThen(diff, "removedKeys"), vset(vs("gone")), ""},
		{"changedKeys", vThen(diff, "changedKeys"), vset(vs("title")), ""},
		{"unchangedKeys", vThen(diff, "unchangedKeys"), vset(vs("owner")), ""},
		{"affectedKeys", vThen(diff, "affectedKeys"), vset(vs("gone"), vs("label"), vs("title")), ""},
		{"affectedKeys is a set", vOpOn(vThen(diff, "affectedKeys"), tokEQ, vLit(vset(vs("title"), vs("label"), vs("gone")))), vb(true), ""},
		{"affectedKeys is not a list", vOpOn(vThen(diff, "affectedKeys"), tokEQ, vLit(vl(vs("gone"), vs("label"), vs("title")))), vb(false), ""},
		{"affectedKeys hasOnly a list", vThen(vThen(diff, "affectedKeys"), "hasOnly", vl(vs("title"), vs("label"), vs("gone"))), vb(true), ""},
		{"a no-op has no affected keys", vThen(vCall(before, "diff", before), "affectedKeys"), vset(), ""},
		{"keys named like prototype members", vThen(vCall(vm("toString", vi(1)), "diff", vm("constructor", vi(1))), "affectedKeys"), vset(vs("constructor"), vs("toString")), ""},
		{"addedKeys(1)", vThen(diff, "addedKeys", vi(1)), Value{}, "Incorrect number of arguments. Received: 1. Expected: map_diff.addedKeys()."},
		{"size on a map diff", vThen(diff, "size"), Value{}, "Function not found error: Name: [size]."},
		{"keys on a map diff", vThen(diff, "keys"), Value{}, "Name: [keys]."},
		{"values on a map diff", vThen(diff, "values"), Value{}, "Name: [values]."},
		{"get on a map diff", vThen(diff, "get", vs("a"), vn()), Value{}, "Name: [get]."},
	})
}

func TestConversions(t *testing.T) {
	runValueCases(t, []valueCase{
		// int(): Pyric conversion-functions and time-math-and-casts.
		{"int('12')", vFn("int", vs("12")), vi(12), ""},
		{"int('-3')", vFn("int", vs("-3")), vi(-3), ""},
		{"int('+3')", vFn("int", vs("+3")), vi(3), ""},
		{"int('007')", vFn("int", vs("007")), vi(7), ""},
		{"int('-0')", vFn("int", vs("-0")), vi(0), ""},
		{"int of the smallest int", vFn("int", vs("-9223372036854775808")), vi(math.MinInt64), ""},
		{"int(2.9) toward zero", vFn("int", vf(2.9)), vi(2), ""},
		{"int(-2.9) toward zero", vFn("int", vf(-2.9)), vi(-2), ""},
		{"int(int)", vFn("int", vi(math.MaxInt64)), vi(math.MaxInt64), ""},
		// production (Rules Test API, S0-A): int(inf) saturates, it is not an error.
		{"int(Infinity) saturates", vFn("int", vf(math.Inf(1))), vi(math.MaxInt64), ""},
		{"int(-Infinity) saturates", vFn("int", vf(math.Inf(-1))), vi(math.MinInt64), ""},
		{"int(1e20) saturates", vFn("int", vf(1e20)), vi(math.MaxInt64), ""},
		{"int(-9.3e18) saturates", vFn("int", vf(-9.3e18)), vi(math.MinInt64), ""},
		{"int(NaN) is 0", vFn("int", vf(math.NaN())), vi(0), ""},
		{"int('1.5')", vFn("int", vs("1.5")), Value{}, "Type conversion error. Argument: [1.5], From type: [string], To type: [int]."},
		{"int('0x10')", vFn("int", vs("0x10")), Value{}, "Argument: [0x10]"},
		{"int(' 1 ')", vFn("int", vs(" 1 ")), Value{}, "Argument: [ 1 ]"},
		{"int('12 ')", vFn("int", vs("12 ")), Value{}, "Argument: [12 ]"},
		{"int('1e3')", vFn("int", vs("1e3")), Value{}, "Argument: [1e3]"},
		{"int('')", vFn("int", vs("")), Value{}, "Argument: [], From type: [string], To type: [int]."},
		{"int('12abc')", vFn("int", vs("12abc")), Value{}, "Argument: [12abc]"},
		{"int out of range", vFn("int", vs("9223372036854775808")), Value{}, "Argument: [9223372036854775808]"},
		{"int('+-1')", vFn("int", vs("+-1")), Value{}, "Argument: [+-1]"},
		{"int(true)", vFn("int", vb(true)), Value{}, "Unsupported operation error. Received: int(bool). Expected: int(int), int(float), int(string)."},
		{"int(null)", vFn("int", vn()), Value{}, "Received: int(null)."},
		{"int('1', 2)", vFn("int", vs("1"), vi(2)), Value{}, "Incorrect number of arguments. Received: 2. Expected: int(int), int(float), int(string)."},
		// float(): Pyric conversion-functions; production reads hexadecimal, which hidane does not.
		{"float('2.0')", vFn("float", vs("2.0")), vf(2), ""},
		{"float(2)", vFn("float", vi(2)), vf(2), ""},
		{"float(1.5)", vFn("float", vf(1.5)), vf(1.5), ""},
		{"float('1e3')", vFn("float", vs("1e3")), vf(1000), ""},
		{"float('.5')", vFn("float", vs(".5")), vf(0.5), ""},
		{"float('5.')", vFn("float", vs("5.")), vf(5), ""},
		{"float('1.5f')", vFn("float", vs("1.5f")), vf(1.5), ""},
		{"float('1.5d')", vFn("float", vs("1.5d")), vf(1.5), ""},
		{"float('0x1p3')", vFn("float", vs("0x1p3")), vf(8), ""},
		{"float(' 2.5')", vFn("float", vs(" 2.5")), vf(2.5), ""},
		{"float('2.5 ')", vFn("float", vs("2.5 ")), vf(2.5), ""},
		{"float('NaN')", vFn("float", vs("NaN")), vf(math.NaN()), ""},
		{"float('Infinity')", vFn("float", vs("Infinity")), vf(math.Inf(1)), ""},
		{"float('+Infinity')", vFn("float", vs("+Infinity")), vf(math.Inf(1)), ""},
		{"float('-Infinity')", vFn("float", vs("-Infinity")), vf(math.Inf(-1)), ""},
		{"float('-1e400') overflows to -Infinity", vFn("float", vs("-1e400")), vf(math.Inf(-1)), ""},
		{"float('inf')", vFn("float", vs("inf")), Value{}, "Type conversion error. Argument: [inf], From type: [string], To type: [float]."},
		{"float('nan')", vFn("float", vs("nan")), Value{}, "Argument: [nan]"},
		{"float('abc')", vFn("float", vs("abc")), Value{}, "Argument: [abc]"},
		{"float('1.2.3')", vFn("float", vs("1.2.3")), Value{}, "Argument: [1.2.3]"},
		{"float('1_000')", vFn("float", vs("1_000")), Value{}, "Argument: [1_000]"},
		{"float('')", vFn("float", vs("")), Value{}, "Argument: []"},
		{"float('0x10')", vFn("float", vs("0x10")), Value{}, "Argument: [0x10]"},
		{"float('NaNf')", vFn("float", vs("NaNf")), Value{}, "Argument: [NaNf]"},
		{"float(true)", vFn("float", vb(true)), Value{}, "Received: float(bool). Expected: float(int), float(float), float(string)."},
		{"float(null)", vFn("float", vn()), Value{}, "Received: float(null)."},
		// string(): Pyric conversion-functions and time-math-and-casts (production), Java's
		// Double.toString for floats.
		{"string(12)", vFn("string", vi(12)), vs("12"), ""},
		{"string(-5)", vFn("string", vi(-5)), vs("-5"), ""},
		{"string(true)", vFn("string", vb(true)), vs("true"), ""},
		{"string(null)", vFn("string", vn()), vs("null"), ""},
		{"string('s')", vFn("string", vs("s")), vs("s"), ""},
		{"string(path)", vFn("string", vp("a", "b")), vs("/a/b"), ""},
		{"string(empty path)", vFn("string", vp()), vs("/"), ""},
		{"string([1])", vFn("string", vl(vi(1))), Value{}, "Unsupported operation error. Received: string(list). Expected: string(int), string(float), string(null), string(string), string(bool), string(path)."},
		{"string({})", vFn("string", vm()), Value{}, "Received: string(map)."},
		{"string(bytes)", vFn("string", vbytes("abc")), Value{}, "Received: string(bytes)."},
		{"string(duration)", vFn("string", vdur(time.Second)), Value{}, "Received: string(duration)."},
		{"string(latlng)", vFn("string", LatLng(1, 2)), Value{}, "Received: string(latlng)."},
		{"string()", vFn("string"), Value{}, "Incorrect number of arguments. Received: 0."},
		// Pyric bool-function: there is no bool().
		{"bool('true')", vFn("bool", vs("true")), Value{}, "Function not found error: Name: [bool]."},
		// debug() does not exist in production's only recording, the Rules Test API (S0-A
		// aruma/errors/debug_function, aruma/storage/debug); provisional until deployed rules
		// are measured.
		{"debug", vFn("debug", vm("a", vi(1))), Value{}, "Function not found error: Name: [debug]."},
		{"debug of two", vFn("debug", vi(1), vi(2)), Value{}, "Function not found error: Name: [debug]."},
	})
}

// string() of a float is Java's Double.toString (production, Pyric conversion-functions and
// time-math-and-casts).
func TestJavaDouble(t *testing.T) {
	for in, want := range map[float64]string{
		1: "1.0", 1.5: "1.5", 2: "2.0", -2.25: "-2.25", 0.30000000000000004: "0.30000000000000004",
		1.0 / 3.0: "0.3333333333333333", 1e6: "1000000.0", 9999999: "9999999.0", 1e7: "1.0E7",
		1e-3: "0.001", 2e-3: "0.002", 1e-4: "1.0E-4", 123456789: "1.23456789E8", -1.5e10: "-1.5E10",
		1e20: "1.0E20", 1e21: "1.0E21", 1e23: "1.0E23", 2.82879384806159e17: "2.82879384806159E17",
		1.7976931348623157e308: "1.7976931348623157E308", 2.2250738585072014e-308: "2.2250738585072014E-308",
		4.9e-324: "4.9E-324", -4.9e-324: "-4.9E-324", 1e-323: "9.9E-324", 2e-323: "2.0E-323",
		1e-322: "9.9E-323", 1e-310: "1.0E-310", negZero: "-0.0", 0: "0.0", 1e300: "1.0E300",
		math.Inf(-1): "-Infinity", 9223372036854775807: "9.223372036854776E18", 100: "100.0",
	} {
		if got := javaDouble(in); got != want {
			t.Errorf("javaDouble(%v) = %q, want %q", in, got, want)
		}
	}
	if got := javaDouble(math.NaN()); got != "NaN" {
		t.Errorf("javaDouble(NaN) = %q", got)
	}
}

func TestMath(t *testing.T) {
	runValueCases(t, []valueCase{
		// Pyric math-namespace and time-math-and-casts (production).
		{"abs(-2) is int", vFn("math.abs", vi(-2)), vi(2), ""},
		{"abs(-2.5) is float", vFn("math.abs", vf(-2.5)), vf(2.5), ""},
		{"abs(-1.0) is float", vFn("math.abs", vf(-1)), vf(1), ""},
		{"abs of the smallest int wraps", vFn("math.abs", vi(math.MinInt64)), vi(math.MinInt64), ""},
		{"ceil(1.5) is a float", vFn("math.ceil", vf(1.5)), vf(2), ""},
		{"ceil(-0.5)", vFn("math.ceil", vf(-0.5)), vf(negZero), ""},
		{"ceil(1) is a float", vFn("math.ceil", vi(1)), vf(1), ""},
		{"floor(-1.5)", vFn("math.floor", vf(-1.5)), vf(-2), ""},
		{"floor(1) is a float", vFn("math.floor", vi(1)), vf(1), ""},
		{"round(2.5)", vFn("math.round", vf(2.5)), vi(3), ""},
		{"round(-2.5) half up", vFn("math.round", vf(-2.5)), vi(-2), ""},
		{"round(-1.5)", vFn("math.round", vf(-1.5)), vi(-1), ""},
		{"round(0.5)", vFn("math.round", vf(0.5)), vi(1), ""},
		{"round(-0.5)", vFn("math.round", vf(-0.5)), vi(0), ""},
		{"round(1.4)", vFn("math.round", vf(1.4)), vi(1), ""},
		{"round(0.49999999999999994)", vFn("math.round", vf(0.49999999999999994)), vi(0), ""},
		{"round(1) is int", vFn("math.round", vi(1)), vi(1), ""},
		{"round(NaN) is 0", vFn("math.round", vf(math.NaN())), vi(0), ""},
		{"round(Infinity) saturates", vFn("math.round", vf(math.Inf(1))), vi(math.MaxInt64), ""},
		{"sqrt(4)", vFn("math.sqrt", vi(4)), vf(2), ""},
		{"sqrt(-1) is NaN", vFn("math.sqrt", vi(-1)), vf(math.NaN()), ""},
		{"pow(2, 3)", vFn("math.pow", vi(2), vi(3)), vf(8), ""},
		{"pow(2, -1)", vFn("math.pow", vi(2), vi(-1)), vf(0.5), ""},
		{"pow(4, 0.5)", vFn("math.pow", vi(4), vf(0.5)), vf(2), ""},
		{"pow(0, -1)", vFn("math.pow", vi(0), vi(-1)), vf(math.Inf(1)), ""},
		{"isNaN(NaN)", vFn("math.isNaN", vf(math.NaN())), vb(true), ""},
		{"isNaN(1)", vFn("math.isNaN", vi(1)), vb(false), ""},
		{"isNaN(Infinity)", vFn("math.isNaN", vf(math.Inf(1))), vb(false), ""},
		{"abs('a')", vFn("math.abs", vs("a")), Value{}, "Unsupported operation error. Received: math.abs(string). Expected: math.abs(int), math.abs(float)."},
		{"abs(true)", vFn("math.abs", vb(true)), Value{}, "Received: math.abs(bool)."},
		{"abs(null)", vFn("math.abs", vn()), Value{}, "Received: math.abs(null)."},
		{"ceil('a')", vFn("math.ceil", vs("a")), Value{}, "Received: math.ceil(string). Expected: math.ceil(float)."},
		{"sqrt('a')", vFn("math.sqrt", vs("a")), Value{}, "Received: math.sqrt(string). Expected: math.sqrt(float)."},
		{"pow(2, 'a')", vFn("math.pow", vi(2), vs("a")), Value{}, "Received: math.pow(int, string). Expected: math.pow(float, float)."},
		{"isNaN('a')", vFn("math.isNaN", vs("a")), Value{}, "Received: math.isNaN(string). Expected: math.isNaN(float)."},
		{"abs(1, 2)", vFn("math.abs", vi(1), vi(2)), Value{}, "Incorrect number of arguments. Received: 2. Expected: math.abs(int), math.abs(float)."},
		{"sqrt()", vFn("math.sqrt"), Value{}, "Incorrect number of arguments. Received: 0. Expected: math.sqrt(float)."},
		// Pyric: math.isInfinite is in the reference, not in production.
		{"isInfinite", vFn("math.isInfinite", vf(1)), Value{}, "Function not found error: Name: [math.isInfinite]."},
	})
	if isLibraryFunction("math.isInfinite") || isLibraryFunction("bool") {
		t.Error("math.isInfinite or bool is a library function")
	}
}

func TestHashing(t *testing.T) {
	hex := func(name string, arg Value) testExpr { return vThen(vFn(name, arg), "toHexString") }
	runValueCases(t, []valueCase{
		// Pyric bytes-toutf8-and-hashing (production): digests are bytes, hex upper case, crc32
		// and crc32c little-endian.
		{"md5('')", hex("hashing.md5", vs("")), vs("D41D8CD98F00B204E9800998ECF8427E"), ""},
		{"sha256('abc')", hex("hashing.sha256", vs("abc")), vs("BA7816BF8F01CFEA414140DE5DAE2223B00361A396177A9CB410FF61F20015AD"), ""},
		{"crc32 little-endian", hex("hashing.crc32", vs("123456789")), vs("2639F4CB"), ""},
		{"crc32c little-endian", hex("hashing.crc32c", vs("123456789")), vs("839206E3"), ""},
		{"md5 RFC 1321", hex("hashing.md5", vs("12345678901234567890123456789012345678901234567890123456789012345678901234567890")), vs("57EDF4A22BE3C955AC49DA2E2107B67A"), ""},
		{"md5 of 55 bytes", hex("hashing.md5", vs(strings.Repeat("x", 55))), vs("04364420E25C512FD958A70738AA8F72"), ""},
		{"md5 non-ASCII", hex("hashing.md5", vs("naïve café 日本語")), vs("2204EF5849F3257174545026F61FFD01"), ""},
		{"sha256 FIPS 180-4", hex("hashing.sha256", vs("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq")), vs("248D6A61D20638B8E5C026930C3E6039A33CE45964FF2167F6ECEDD419DB06C1"), ""},
		{"sha256 non-ASCII", hex("hashing.sha256", vs("naïve café 日本語")), vs("7EDCE543470723527A9A231BD4C4EFA1B626CE8232318DCB1AA1AE9B43766867"), ""},
		{"a string hashes as its UTF-8", vOpOn(vFn("hashing.md5", vbytes("abc")), tokEQ, vFn("hashing.md5", vs("abc"))), vb(true), ""},
		{"a digest is bytes", vOpOn(vFn("hashing.sha256", vs("a")), tokEQ, vLit(vs("a"))), vb(false), ""},
		{"md5(int)", vFn("hashing.md5", vi(1)), Value{}, "Unsupported operation error. Received: hashing.md5(int). Expected: hashing.md5(bytes), hashing.md5(string)."},
		{"crc32()", vFn("hashing.crc32"), Value{}, "Incorrect number of arguments. Received: 0."},
	})
}

func TestLatLng(t *testing.T) {
	p := LatLng(37.42, -122.08)
	runValueCases(t, []valueCase{
		// Pyric duration-and-latlng; distance's radius is hidane's (emulator).
		{"value", vFn("latlng.value", vf(37.42), vf(-122.08)), p, ""},
		{"ints", vFn("latlng.value", vi(1), vi(2)), LatLng(1, 2), ""},
		{"latitude", vCall(p, "latitude"), vf(37.42), ""},
		{"longitude", vCall(p, "longitude"), vf(-122.08), ""},
		{"distance to itself", vCall(p, "distance", p), vf(0), ""},
		{"latitude below -90", vFn("latlng.value", vf(-100), vf(-122.08)), Value{}, "Latitude should be in [-90,90] range and longitude should be in [-180,180] range. latitude: -100.0, longitude: -122.08."},
		{"longitude past 180", vFn("latlng.value", vi(0), vf(180.5)), Value{}, "longitude: 180.5."},
		{"NaN", vFn("latlng.value", vf(math.NaN()), vi(0)), Value{}, "latitude: NaN"},
		{"a string", vFn("latlng.value", vs("1"), vi(0)), Value{}, "Received: latlng.value(string, int). Expected: latlng.value(float, float)."},
		{"distance to an int", vCall(p, "distance", vi(1)), Value{}, "Received: latlng.distance(int). Expected: latlng.distance(latlng)."},
		{"latitude(1)", vCall(p, "latitude", vi(1)), Value{}, "Expected: latlng.latitude()."},
		{"no such method", vCall(p, "lat"), Value{}, "Name: [lat]."},
	})
	d, err := callMethod(newTestMeter(), LatLng(0, 0), "distance", []Value{LatLng(0, 90)})
	if want := math.Pi / 2 * earthRadius; err != nil || math.Abs(d.float()-want) > 1e-6 {
		t.Errorf("a quarter of the equator = %s, %v; want %v meters", showValue(d), err, want)
	}
}
