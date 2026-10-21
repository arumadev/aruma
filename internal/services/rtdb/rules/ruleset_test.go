package rules

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseErrorsArePositioned(t *testing.T) {
	cases := []struct {
		name, doc string
		line, col int
		path, msg string
	}{
		{"expression on a later line", "{\n  \"rules\": {\n    \"a\": {\n      \".write\": \"newData.val() == \"\n    }\n  }\n}",
			4, 35, "rules/a/.write", "Unexpected end of expression"},
		{"after an escape", `{"rules":{".read":"data.child(\"x\").val() == foo"}}`, 1, 47, "rules/.read", "Unknown variable 'foo'."},
		{"after a unicode escape", "{\"rules\":{\".read\":\"'\\u00e9' == bar\"}}", 1, 32, "rules/.read", "Unknown variable 'bar'."},
		{"after a non-ASCII character in a rule", `{"rules":{".read":"'é' == bar"}}`, 1, 27, "rules/.read", "Unknown variable 'bar'."},
		{"after a non-ASCII character", `{"rules":{"é":{".read":"x"}}}`, 1, 25, "rules/é/.read", "Unknown variable 'x'."},
		{"a regex", `{"rules":{".read":"auth.uid.matches(/a{/)"}}`, 1, 37, "rules/.read", "Illegal regular expression, integer expected at position 3"},
		{"a document error", `{"rules":{".read":1}}`, 1, 19, "rules/.read", "Invalid rule expression.  Expected 'true', 'false', or an expression string."},
		{"unsupported getPriority", `{"rules":{"$k":{".validate":"newData.getPriority() != null"}}}`, 1, 30, "rules/$k/.validate", "getPriority() is not supported."},
		{"newData in .read", `{"rules":{"a":{"b":{".read":"newData.exists()"}}}}`, 1, 30, "rules/a/b/.read", "newData is invalid in .read expressions."},
		{"a comment before", "// my rules\n{\"rules\":{\".read\":\"nope\"}}", 2, 20, "rules/.read", "Unknown variable 'nope'."},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.doc))
			var first *Error
			switch e := err.(type) {
			case *Error:
				first = e
			case Errors:
				first = e[0]
			default:
				t.Fatalf("error %T %v", err, err)
			}
			if first.Line != c.line || first.Column != c.col || first.Path != c.path || first.Msg != c.msg {
				t.Fatalf("got %d:%d %s %q", first.Line, first.Column, first.Path, first.Msg)
			}
		})
	}
}

func TestParseReportsEveryBadExpression(t *testing.T) {
	_, err := Parse([]byte(`{"rules":{".read":"foo","a":{".write":"bar"},"b":{".validate":"true"}}}`))
	var es Errors
	if !errors.As(err, &es) || len(es) != 2 {
		t.Fatalf("%T %v", err, err)
	}
	want := "1:20: Unknown variable 'foo'. (rules/.read)\n1:40: Unknown variable 'bar'. (rules/a/.write)"
	if err.Error() != want {
		t.Fatalf("%q", err.Error())
	}
}

func TestParseLimits(t *testing.T) {
	big := `{"rules":{".read":"` + strings.Repeat("true && ", MaxSize/8) + `true"}}`
	if _, err := Parse([]byte(big)); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversized: %v", err)
	}
	deepDoc := `{"rules":` + strings.Repeat(`{"a":`, 100) + `{}` + strings.Repeat(`}`, 101)
	if _, err := Parse([]byte(deepDoc)); err == nil || !strings.Contains(err.Error(), "nested too deeply") {
		t.Fatalf("deep document: %v", err)
	}
	deepExpr := `{"rules":{".read":"` + strings.Repeat("(", 5000) + "true" + strings.Repeat(")", 5000) + `"}}`
	if _, err := Parse([]byte(deepExpr)); err == nil || !strings.Contains(err.Error(), "nested too deeply") {
		t.Fatalf("deep expression: %v", err)
	}
	unary := `{"rules":{".read":"` + strings.Repeat("!", 5000) + `true"}}`
	if _, err := Parse([]byte(unary)); err == nil || !strings.Contains(err.Error(), "nested too deeply") {
		t.Fatalf("deep unary: %v", err)
	}
	// Many errors are capped.
	var b strings.Builder
	b.WriteString(`{"rules":{`)
	for i := 0; i < 100; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"k` + strings.Repeat("x", i) + `":{".read":"nope"}`)
	}
	b.WriteString(`}}`)
	var es Errors
	if _, err := Parse([]byte(b.String())); !errors.As(err, &es) || len(es) != maxErrors {
		t.Fatalf("%d errors", len(es))
	}
}

func TestParseAcceptsFirebaseLooseness(t *testing.T) {
	doc := "/* rules */\n{\n  // public\n  \"rules\": {\n    \".read\": \"true\", // trailing\n    \"a\": {\n      \".write\": \"auth != null &&\n        auth.uid == 'x';\",\n    },\n  },\n}\n"
	if _, err := Parse([]byte(strings.Replace(doc, "&&\n", "&& ", 1))); err != nil {
		t.Fatal(err)
	}
}

// The parse and evaluation of one expression, end to end, for what neither the fixtures nor the
// emulator matrix pin down.
func TestExpressions(t *testing.T) {
	cases := []struct {
		expr string
		want any // true, false, or "fail"
	}{
		{"(1 + 2) * 3 == 9", true},
		{"1 + 2 * 3 == 7", true},
		{"10 - 2 - 3 == 5", true},
		{"!false && !(1 > 2)", true},
		{"true ? false : true", false},
		{"false ? true : false ? true : true", true},
		{"-(-3) == 3", true},
		{"- 3 == -3", true},
		{"'a' + 'b' + 1 + 2 == 'ab12'", true},
		{"1 + 2 + 'a' == '3a'", true},
		{"'x'.length", "load"},
		{"'abc'.replace('b', 'B').toUpperCase() == 'ABC'", true},
		{"'a' == 'a' && 'a' != 'b'", true},
		{"2 >= 2 && 2 <= 2 && !(2 < 2) && !(2 > 2)", true},
		{"'b' >= 'a'", true},
		{"now > 0 && now == now", true},
		{"$k == 'key'", true},
		{"$k + 1 == 'key1'", true},
		{"root.child($k).exists() == false", true},
		{"query.orderByPriority", true},
		{"auth.missing.contains('x')", "fail"},
		{"1/0 != 1/0", true},
	}
	for _, c := range cases {
		got, lerr, rerr := evalExpr(c.expr, kindRead, map[string]string{"$k": "key"}, Unauthenticated(), nil, PlainListen)
		switch {
		case c.want == "load":
			if lerr == nil {
				t.Errorf("%s: loads", c.expr)
			}
		case lerr != nil:
			t.Errorf("%s: %v", c.expr, lerr)
		case c.want == "fail":
			if rerr == nil {
				t.Errorf("%s = %v, want a failure", c.expr, got)
			}
		case rerr != nil:
			t.Errorf("%s: %v", c.expr, rerr)
		case got != c.want:
			t.Errorf("%s = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestJavaNumber(t *testing.T) {
	tenth, fifth := 0.1, 0.2
	if got := javaNumber(tenth + fifth); got != "0.30000000000000004" {
		t.Errorf("0.1 + 0.2 = %s", got)
	}
	for f, want := range map[float64]string{
		0: "0", 1: "1", -5: "-5", 0.5: "0.5", 100: "100", 1e7: "10000000", 1.5e7: "15000000",
		12345678.5: "1.23456785E7", 1e-3: "0.001", 1e-4: "1.0E-4", 1.5e-7: "1.5E-7", 1e21: "1.0E21",
		9.2e18: "9200000000000000000", 1e19: "1.0E19", -1e19: "-1.0E19",
		-0.25: "-0.25", 123.456: "123.456",
	} {
		if got := javaNumber(f); got != want {
			t.Errorf("javaNumber(%v) = %s, want %s", f, got, want)
		}
	}
}

func TestJavaStrings(t *testing.T) {
	if compareUTF16(string(rune(0xffff)), "😀") <= 0 || compareUTF16("a", "ab") >= 0 || compareUTF16("b", "b") != 0 {
		t.Error("compareUTF16")
	}
	for in, want := range map[string]string{"straße": "STRASSE", "ﬁx": "FIX", "ǅ": "Ǆ", "abc": "ABC"} {
		if got := javaUpper(in); got != want {
			t.Errorf("upper %s = %s", in, got)
		}
	}
	for in, want := range map[string]string{"ΣΑΣ": "σας", "ΣΑΣ ΣΑΣ": "σας σας", "Σ": "σ", "İ": "i" + string(rune(0x307)), "ABC": "abc"} {
		if got := javaLower(in); got != want {
			t.Errorf("lower %s = %s", in, got)
		}
	}
	if utf16Len("a😀é") != 4 {
		t.Error("utf16Len")
	}
}

func TestRegexTranslation(t *testing.T) {
	cases := []struct {
		lit     string
		matches map[string]bool
	}{
		{"/abc/", map[string]bool{"xabcx": true, "ab": false}},
		{"/^abc$/", map[string]bool{"abc": true, "xabc": false, "abcx": false}},
		{"/abc/i", map[string]bool{"ABC": true}},
		{"/a.b/", map[string]bool{"a\nb": true, "ab": false}},
		{`/a\sb/`, map[string]bool{"a b": true, "a\vb": true, "ab": false}},
		{`/a\Sb/`, map[string]bool{"a b": false, "axb": true}},
		{`/[\s]/`, map[string]bool{"\t": true, "x": false}},
		{`/^\d+$/`, map[string]bool{"123": true, "١": false}},
		{`/a(?:b)/`, map[string]bool{"ab": false, "a?:b": true}},
		{`/a\1/`, map[string]bool{"a1": true}},
		{`/a\bb/`, map[string]bool{"abb": true}},
		{`/^a+?$/`, map[string]bool{"": true, "aaa": true}},
		{`/^a**$/`, map[string]bool{"": true, "aa": true}},
		{`/^a{2}$/`, map[string]bool{"aa": true, "a{2}": false}},
		{`/^a{2,}$/`, map[string]bool{"aaaa": true, "a": false}},
		{`/^a{1,2}b$/`, map[string]bool{"aab": true, "aaab": false}},
		{`/^+a$/`, map[string]bool{"+a": true}},
		{`/^(*a)$/`, map[string]bool{"*a": true}},
		{`/^{a}$/`, map[string]bool{"{a}": true}},
		{`/^[]a]+$/`, map[string]bool{"]a": true}},
		{`/^[a-]$/`, map[string]bool{"-": true}},
		{`/^[\d-z]$/`, map[string]bool{"-": true, "5": true, "y": false}},
		{`/^[^a-c]$/`, map[string]bool{"d": true, "b": false}},
		{`/^[\]]$/`, map[string]bool{"]": true}},
		{`/^\$\^$/`, map[string]bool{"$^": true}},
		{`/^a\/b$/`, map[string]bool{"a/b": true}},
		{`/^()$/`, map[string]bool{"": true}},
		{`/^(a|b)c$/`, map[string]bool{"bc": true}},
		{`/^é+$/i`, map[string]bool{"ÉÉ": true}},
	}
	for _, c := range cases {
		re, err := translateRegex(c.lit)
		if err != nil {
			t.Errorf("%s: %v", c.lit, err)
			continue
		}
		for s, want := range c.matches {
			if re.MatchString(s) != want {
				t.Errorf("%s (as %s) on %q: %v, want %v", c.lit, re, s, !want, want)
			}
		}
	}
	for _, bad := range []string{"/a/g", "/a/ii", "/a^/", "/a$b/", "/(^a)/", "/a$|b/", "/(a|)/", "/a||b/", "/a{/", "/a{x}/",
		"/a{3,2}/", "/a{2000}/", "/(a/", "/a)/", "/[a/", "/[z-a]/", `/a\/`, "/a|*b/", "/a(?=b)|*/"} {
		if _, err := translateRegex(bad); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
}

// matches() is bounded by maxMatchCost, the string's length times the pattern's program size:
// past it the rule fails, which denies, instead of holding the writer. /[a-z]{1000}$/ on a
// megabyte of "a" keeps a thousand threads alive at every byte: unbounded, the write was
// allowed after 35 seconds on an Apple M1; bounded, it is denied at once. (Right at
// the bound, 66908 bytes of "a", the match takes 0.9 s on the M1, which the race detector
// makes 30 s, so the bound itself is checked on a string the pattern rejects early.)
func TestMatchesIsBounded(t *testing.T) {
	const pattern = `/[a-z]{1000}$/`
	rs, err := Parse([]byte(`{"rules":{"s":{".write":"newData.isString() && newData.val().matches(` + pattern + `)"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	src, _ := NewMemorySource(nil)
	write := func(s string) (bool, time.Duration) {
		t.Helper()
		start := time.Now()
		ok, err := rs.CanWrite(context.Background(), Env{Data: src}, Change{Path: []string{"s"}, Value: s})
		if err != nil {
			t.Fatal(err)
		}
		return ok, time.Since(start)
	}
	if ok, took := write(strings.Repeat("a", 1<<20)); ok || took > time.Second {
		t.Errorf("1 MB, past the bound: %v in %v, want denied at once", ok, took)
	}
	if ok, _ := write(strings.Repeat("a", 2000)); !ok {
		t.Error("2000 bytes, within the bound: denied")
	}
	re, _ := compileRuleRegex(pattern)
	fits := maxMatchCost / re.size
	if ok, err := re.match(strings.Repeat("0", fits)); ok || err != nil {
		t.Errorf("%d bytes, cost %d: %v %v, want a match that fails", fits, fits*re.size, ok, err)
	}
	if _, err := re.match(strings.Repeat("0", fits+1)); err == nil {
		t.Errorf("%d bytes, past the bound: no error", fits+1)
	}
}

func TestNowDefaultsToTheClock(t *testing.T) {
	rs, _ := Parse([]byte(`{"rules":{".read":"now > 1700000000000"}}`))
	src, _ := NewMemorySource(nil)
	ok, err := rs.CanRead(t.Context(), Env{Data: src}, nil, PlainListen)
	if err != nil || !ok || time.Now().Year() < 2023 {
		t.Fatalf("%v %v", ok, err)
	}
}
