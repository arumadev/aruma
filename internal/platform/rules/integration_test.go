package rules

import (
	"strings"
	"testing"
)

// The tests here go through the whole engine, source text to decision: they need the parser,
// the checker, the interpreter and the library together (S1-E).

// compileSource compiles a Firestore source and fails the test on an error.
func compileSource(t *testing.T, src string) (*Ruleset, Issues) {
	t.Helper()
	rs, issues := Compile(Firestore, File{Name: "firestore.rules", Content: src}, DefaultLimits())
	if rs == nil {
		t.Fatalf("Compile: %v", issues)
	}
	return rs, issues
}

// grantingSource returns a Firestore source granting get on /c/{id} when cond holds.
func grantingSource(cond string) string {
	return "rules_version = '2';\nservice cloud.firestore {\n  match /databases/{database}/documents {\n" +
		"    match /c/{id} {\n      allow get: if " + cond + ";\n    }\n  }\n}\n"
}

// Map and list literals are built by the library, so keys() is sorted and values() keeps the
// written order, a repeated key keeping its first place and its last value (Rules Test API:
// aruma/collections/map_keys_order_sorted, map_values_order; Pyric keyOrder).
func TestLiteralsKeepTheWrittenOrder(t *testing.T) {
	for cond, want := range map[string]bool{
		"{'b': 1, 'a': 2}.keys() == ['a', 'b']":                true,
		"{'b': 1, 'a': 2}.keys() == ['b', 'a']":                false,
		"{'b': 1, 'a': 2}.values() == [1, 2]":                  true,
		"{'b': 1, 'a': 2}.values() == [2, 1]":                  false,
		"{'b': 1, 'a': 2, 'b': 3}.values() == [3, 2]":          true,
		"{'b': 1, 'a': 2, 'b': 3}.size() == 2":                 true,
		"[3, 1, 2] == [3, 1, 2] && [3, 1, 2][0] == 3":          true,
		"{'z': [2, 1], 'y': 0}.values() == [[2, 1], 0]":        true,
		"{'b': {'d': 1, 'c': 2}}.b.values() == [1, 2]":         true,
		"{'b': {'d': 1, 'c': 2}}.values()[0].keys()[0] == 'c'": true,
	} {
		rs, _ := compileSource(t, grantingSource(cond))
		if d := evaluate(t, rs, request(MethodGet, docPath("c", "d"), nil)); d.Allow != want {
			t.Errorf("%s: allow %v, want %v (%+v)", cond, d.Allow, want, d.Statements)
		}
	}
}

// A file with a syntax error is not checked: production reports the syntax errors alone, not
// the let of rules_version 1, the recursion or the old version's warning that the checker
// would add (S1-C review, probe4 on the Rules Test API).
func TestSyntaxErrorSkipsTheCheck(t *testing.T) {
	for name, c := range map[string]struct {
		line int
		src  string
	}{
		"let in version 1": {7, "service cloud.firestore {\n  match /databases/{database}/documents {\n" +
			"    function f() {\n      let a = 1;\n      return a == 1;\n    }\n" +
			"    match /c/{id} { allow get: if f() == ; }\n  }\n}\n"},
		"recursion": {5, "rules_version = '2';\nservice cloud.firestore {\n  match /databases/{database}/documents {\n" +
			"    function f() { return f(); }\n" +
			"    match /c/{id} { allow get: if f() == ; }\n  }\n}\n"},
	} {
		rs, issues := Compile(Firestore, File{Name: "firestore.rules", Content: c.src}, DefaultLimits())
		if rs != nil || issues.Err() == nil {
			t.Fatalf("%s: compiled: %v", name, issues)
		}
		for _, is := range issues {
			if is.Severity != SeverityError || is.Position == nil || is.Position.Line != c.line {
				t.Errorf("%s: an issue of the checker: %+v", name, is)
			}
			for _, checker := range []string{msgLetVersion1, msgOldVersion, msgRecursive, "Unused"} {
				if strings.Contains(is.Description, checker) {
					t.Errorf("%s: the checker ran: %s", name, is.Description)
				}
			}
		}
	}
}

// debug() with other than one argument compiles with production's warning at its name, and
// fails when evaluated as every debug() does on the Rules Test API (S1-B review). Both are
// provisional (docs/compat/rules-v0.3.md).
func TestDebugArity(t *testing.T) {
	for cond, warns := range map[string]bool{
		"debug(1, 2) == 1": true,
		"debug() == 1":     true,
		"debug(1) == 1":    false,
	} {
		rs, issues := compileSource(t, grantingSource(cond))
		var got []string
		for _, is := range issues {
			if is.Severity == SeverityWarning {
				got = append(got, is.Description)
			}
		}
		const want = "Incorrect number of arguments supplied to function: debug."
		if warned := len(got) == 1 && got[0] == want; warned != warns || !warns && len(got) != 0 {
			t.Errorf("%s: warnings %q, want %q: %v", cond, got, want, warns)
		}
		if warns && issues[0].Position.Column != len("      allow get: if ")+1 {
			t.Errorf("%s: warned at %+v, want debug's name", cond, issues[0].Position)
		}
		d := evaluate(t, rs, request(MethodGet, docPath("c", "d"), nil))
		if d.Allow || len(d.Statements) != 1 || d.Statements[0].Message != "Function not found error: Name: [debug]." {
			t.Errorf("%s: %+v", cond, d.Statements)
		}
	}
	// Measured on the Rules Test API too (S1-E probe arity.mjs): the warning stands at the name.
	for cond, at := range map[string]string{
		"math.abs(1, 2) == 1": "math.abs",
		"'a'.size(1) == 1":    "'a'.size",
		"[1].size(1) == 1":    "[1].size",
		"'a'.lower(1) == 'a'": "'a'.lower",
		"'a'.size() == 1":     "",
	} {
		_, issues := compileSource(t, grantingSource(cond))
		if at == "" {
			if len(issues) != 0 {
				t.Errorf("%s: %+v", cond, issues)
			}
			continue
		}
		name := at[strings.LastIndexAny(at, ".")+1:]
		if at == "math.abs" {
			name = at
		}
		if len(issues) != 1 || issues[0].Description != "Incorrect number of arguments supplied to function: "+name+"." ||
			issues[0].Position.Column != len("      allow get: if ")+strings.LastIndex(at, ".")+2 {
			t.Errorf("%s: %+v", cond, issues)
		}
	}
}

// An invalid literal pattern fails compilation in matches() and split(), and only evaluation in
// replace(), where || absorbs it (Rules Test API, S1-E probe, 2026-10-24).
func TestInvalidLiteralPatterns(t *testing.T) {
	for cond, compiles := range map[string]bool{
		"'a'.matches('(')":                         false,
		"'a(b'.split('(').size() == 2":             false,
		"'ab'.split('(?=b)').size() == 2":          false,
		"'abc'.replace('(', 'x') == 'abc'":         true,
		"'abc'.replace('(', 'x') == 'abc' || true": true,
	} {
		rs, issues := Compile(Firestore, File{Name: "firestore.rules", Content: grantingSource(cond)}, DefaultLimits())
		if (rs != nil) != compiles {
			t.Errorf("%s: compiled %v, want %v: %v", cond, rs != nil, compiles, issues)
			continue
		}
		if !compiles {
			if len(issues) != 1 || !strings.HasPrefix(issues[0].Description, "Invalid regular expression pattern. Pattern: ") {
				t.Errorf("%s: %v", cond, issues)
			}
			continue
		}
		d := evaluate(t, rs, request(MethodGet, docPath("c", "d"), nil))
		if want := strings.HasSuffix(cond, "|| true"); d.Allow != want {
			t.Errorf("%s: allow %v, want %v: %+v", cond, d.Allow, want, d.Statements)
		}
		if !d.Allow && d.Statements[0].Message != "Invalid regular expression pattern. Pattern: (." {
			t.Errorf("%s: %+v", cond, d.Statements)
		}
	}
	// A literal past Aruma's instruction limit fails compilation in replace() too, || true or not:
	// the limit is ours, not production's syntax.
	huge := strings.Repeat("(a|b|c|d|e|f|g|h)", 2000)
	for _, m := range []string{"matches('" + huge + "')", "replace('" + huge + "', 'x') == 'a' || true"} {
		rs, issues := Compile(Firestore, File{Name: "firestore.rules", Content: grantingSource("'a'." + m)}, DefaultLimits())
		if rs != nil || len(issues) != 1 || !strings.HasPrefix(issues[0].Description, "Regular expression is too complex to compile safely") {
			t.Errorf("%.30s…: compiled %v: %v", m, rs != nil, issues)
		}
	}
}
