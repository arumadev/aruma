package rules

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// syntaxParse parses src with the default limits and fails on any issue.
func syntaxParse(t *testing.T, src string) *file {
	t.Helper()
	f, issues := parse("t.rules", src, DefaultLimits())
	if f == nil || len(issues) > 0 {
		t.Fatalf("parse(%q): %v", src, syntaxIssues(issues))
	}
	return f
}

// syntaxCond parses cond as the condition of an allow statement.
func syntaxCond(t *testing.T, cond string) expr {
	t.Helper()
	f := syntaxParse(t, "rules_version = '2';\nservice cloud.firestore {\n  match /a {\n    allow read: if "+cond+";\n  }\n}\n")
	return f.body.matches[0].body.allows[0].cond
}

// syntaxIssues prints issues as the compiler's error message lists them, "L5:22 message".
func syntaxIssues(issues []Issue) string {
	var lines []string
	for _, i := range issues {
		if i.Position == nil {
			lines = append(lines, "L0:0 "+i.Description)
			continue
		}
		lines = append(lines, fmt.Sprintf("L%d:%d %s", i.Position.Line, i.Position.Column, i.Description))
	}
	return strings.Join(lines, "\n")
}

// syntaxShape prints an expression with its structure made explicit.
func syntaxShape(e expr) string {
	switch e := e.(type) {
	case *litExpr:
		switch e.val.Kind() {
		case KindString:
			s, _ := e.val.AsString()
			return strconv.Quote(s)
		case KindBytes:
			b, _ := e.val.AsBytes()
			return fmt.Sprintf("b%q", b)
		case KindNull:
			return "null"
		case KindBool:
			b, _ := e.val.AsBool()
			return strconv.FormatBool(b)
		case KindInt:
			i, _ := e.val.AsInt()
			return strconv.FormatInt(i, 10)
		case KindFloat:
			f, _ := e.val.AsFloat()
			return strconv.FormatFloat(f, 'g', -1, 64) + "f"
		}
	case *identExpr:
		return e.name
	case *memberExpr:
		return syntaxShape(e.x) + "." + e.name
	case *indexExpr:
		return syntaxShape(e.x) + "[" + syntaxShape(e.i) + "]"
	case *sliceExpr:
		return syntaxShape(e.x) + "[" + syntaxShape(e.lo) + ":" + syntaxShape(e.hi) + "]"
	case *callExpr:
		var args []string
		for _, a := range e.args {
			args = append(args, syntaxShape(a))
		}
		return syntaxShape(e.fn) + "(" + strings.Join(args, ", ") + ")"
	case *unaryExpr:
		return "(" + e.op.String() + syntaxShape(e.x) + ")"
	case *binaryExpr:
		return "(" + syntaxShape(e.x) + " " + e.op.String() + " " + syntaxShape(e.y) + ")"
	case *isExpr:
		return "(" + syntaxShape(e.x) + " is " + e.typ + ")"
	case *condExpr:
		return "(" + syntaxShape(e.test) + " ? " + syntaxShape(e.yes) + " : " + syntaxShape(e.no) + ")"
	case *listExpr:
		var elems []string
		for _, x := range e.elems {
			elems = append(elems, syntaxShape(x))
		}
		return "[" + strings.Join(elems, ", ") + "]"
	case *mapExpr:
		var entries []string
		for i := range e.keys {
			entries = append(entries, syntaxShape(e.keys[i])+": "+syntaxShape(e.vals[i]))
		}
		return "{" + strings.Join(entries, ", ") + "}"
	case *pathExpr:
		var b strings.Builder
		for _, s := range e.segs {
			if s.x != nil {
				b.WriteString("/$(" + syntaxShape(s.x) + ")")
			} else {
				b.WriteString("/" + s.text)
			}
		}
		return b.String()
	}
	return fmt.Sprintf("?%T", e)
}

// Precedence follows the documentation's table (rules-language, "Operators").
func TestParsePrecedence(t *testing.T) {
	for _, c := range []struct{ cond, want string }{
		{"a || b && c == d + e * f", "(a || (b && (c == (d + (e * f)))))"},
		{"a && b || c && d", "((a && b) || (c && d))"},
		{"1 == 1 == true", "((1 == 1) == true)"},
		{"a - b - c", "((a - b) - c)"},
		{"a / b * c % d", "(((a / b) * c) % d)"},
		{"!a.b(c)[0]", "(!a.b(c)[0])"},
		{"!!a", "(!(!a))"},
		{"!-a", "(!(-a))"},
		{"-1 - -2.5 % 3", "((-1) - ((-2.5f) % 3))"},
		{"a < b == c >= d", "((a < b) == (c >= d))"},
		// Relational operators bind tighter than in, in than is, is than the equalities.
		{"a < b in c", "((a < b) in c)"},
		{"a == b in c", "(a == (b in c))"},
		{"a in b is bool", "((a in b) is bool)"},
		{"a == b is bool", "(a == (b is bool))"},
		{"x is string && y in [1, 2,]", "((x is string) && (y in [1, 2]))"},
		{"x is int is bool", "((x is int) is bool)"},
		// ?: is the loosest and right-associative, and a branch may be a conditional.
		{"a ? b : c ? d : e", "(a ? b : (c ? d : e))"},
		{"a ? b ? c : d : e", "(a ? (b ? c : d) : e)"},
		{"a || b ? c && d : e", "((a || b) ? (c && d) : e)"},
		{"(a || b) && c", "((a || b) && c)"},
		{"-(a + b)", "(-(a + b))"},
		{"-9223372036854775808", "-9223372036854775808"},
		{"- 9223372036854775808", "-9223372036854775808"},
		{"-1", "(-1)"},
	} {
		if got := syntaxShape(syntaxCond(t, c.cond)); got != c.want {
			t.Errorf("%s: %s, want %s", c.cond, got, c.want)
		}
	}
}

func TestParsePrimaries(t *testing.T) {
	for _, c := range []struct{ cond, want string }{
		{"null == true && false != 1", "((null == true) && (false != 1))"},
		{"1 + 1.5 + 1. + .5 + 007", "((((1 + 1.5f) + 1f) + 0.5f) + 7)"},
		{`'a' + "b" + 'it\'s' + "\u00e9"`, `((("a" + "b") + "it's") + "é")`},
		{`b'\x01' == B"a"`, `(b"\x01" == b"a")`},
		{"[] == [1, [2], {}]", "([] == [1, [2], {}])"},
		{"{'a': [1, {}], 'b': null,} != {}", `({"a": [1, {}], "b": null} != {})`},
		{"{1: 2}[1] == 2", "({1: 2}[1] == 2)"},
		{"'abc'[1:2] == 'b'", `("abc"[1:2] == "b")`},
		{"a[b][c:d][e]", "a[b][c:d][e]"},
		{"get(/a/b).data.x", "get(/a/b).data.x"},
		{"f() && g(1, 2,) && h(1)", "((f() && g(1, 2)) && h(1))"},
		{"math.abs(1) + duration.value(1, 's')", `(math.abs(1) + duration.value(1, "s"))`},
		{"request.resource.data.keys().hasAll(['a'])", `request.resource.data.keys().hasAll(["a"])`},
		{"firestore.exists(/databases/$(database)/documents/u/$(request.auth.uid))",
			"firestore.exists(/databases/$(database)/documents/u/$(request.auth.uid))"},
		{"/databases/$(database)/documents/(default)/a%20b == /a+b/c:d/e@f", "(/databases/$(database)/documents/(default)/a%20b == /a+b/c:d/e@f)"},
		{"/a/$(b + '/c')", `/a/$((b + "/c"))`},
		// A field may be named by any keyword but rules_version; identifiers may hold $.
		{"request.if == r.while && x.in == x.is && x.null", "(((request.if == r.while) && (x.in == x.is)) && x.null)"},
		{"$x.y == a$", "($x.y == a$)"},
		{"[1,2][0]", "[1, 2][0]"},
	} {
		if got := syntaxShape(syntaxCond(t, c.cond)); got != c.want {
			t.Errorf("%s: %s, want %s", c.cond, got, c.want)
		}
	}
}

func TestParseDeclarations(t *testing.T) {
	src := "rules_version = \"2\";\n" +
		"function top() { return true; }\n" +
		"service cloud.firestore {\n" +
		"  function inService(a) { return a; }\n" +
		"  match /databases/{database}/documents {\n" +
		"    function f(a, b) { let c = a; let d = c; return d == b; }\n" +
		"    match /users/{user}/{rest=**} {\n" +
		"      allow read, write: if f(user, 'x');\n" +
		"      allow get\n" +
		"    }\n" +
		"    match /{single=*}/b {\n" +
		"      allow list, foo, read;\n" +
		"    }\n" +
		"  }\n" +
		"}\n"
	f := syntaxParse(t, src)
	if f.version != "2" || src[f.versionAt.start:f.versionAt.end] != "rules_version" {
		t.Errorf("version %q at %v", f.version, f.versionAt)
	}
	if f.service != "cloud.firestore" || src[f.serviceAt.start:f.serviceAt.end] != "cloud.firestore" {
		t.Errorf("service %q at %v", f.service, f.serviceAt)
	}
	if f.name != "t.rules" || f.src != src || f.span != (span{0, len(src)}) {
		t.Errorf("file %q, span %v", f.name, f.span)
	}
	if len(f.functions) != 1 || f.functions[0].name.text != "top" || len(f.body.functions) != 1 {
		t.Fatalf("functions: %d top-level, %d in the service", len(f.functions), len(f.body.functions))
	}
	docs := f.body.matches[0]
	if got := syntaxPatterns(docs.path); got != "databases {database} documents" {
		t.Errorf("path %s", got)
	}
	fn := docs.body.functions[0]
	if fn.name.text != "f" || len(fn.params) != 2 || fn.params[1].text != "b" || len(fn.lets) != 2 ||
		fn.lets[1].name.text != "d" || syntaxShape(fn.lets[1].value) != "c" || syntaxShape(fn.ret) != "(d == b)" {
		t.Errorf("function %+v", fn)
	}
	if got := src[fn.span.start:fn.span.end]; got != "function f(a, b) { let c = a; let d = c; return d == b; }" {
		t.Errorf("function span %q", got)
	}
	if got := src[fn.lets[0].span.start:fn.lets[0].span.end]; got != "let c = a;" {
		t.Errorf("let span %q", got)
	}
	users := docs.body.matches[0]
	if got := syntaxPatterns(users.path); got != "users {user} {rest=**}" {
		t.Errorf("path %s", got)
	}
	if got := src[users.span.start:users.span.end]; !strings.HasPrefix(got, "match /users") || !strings.HasSuffix(got, "}") {
		t.Errorf("match span %q", got)
	}
	read := users.body.allows[0]
	if len(read.methods) != 2 || read.methods[0].text != "read" || read.methods[1].text != "write" ||
		syntaxShape(read.cond) != `f(user, "x")` {
		t.Errorf("allow %+v", read)
	}
	if got := src[read.span.start:read.span.end]; got != "allow read, write: if f(user, 'x');" {
		t.Errorf("allow span %q", got)
	}
	if get := users.body.allows[1]; get.cond != nil || get.methods[0].text != "get" {
		t.Errorf("an allow without condition: %+v", get)
	}
	single := docs.body.matches[1]
	if got := syntaxPatterns(single.path); got != "{single} b" {
		t.Errorf("{x=*} is a variable: %s", got)
	}
	var methods []string
	for _, m := range single.body.allows[0].methods {
		methods = append(methods, m.text)
	}
	if strings.Join(methods, ",") != "list,foo,read" {
		t.Errorf("methods are kept as written: %v", methods)
	}
	// The segments' spans and the variables' names.
	rest := users.path[2]
	if got := src[rest.start:rest.end]; got != "/{rest=**}" {
		t.Errorf("segment span %q", got)
	}
	if at := patternNameAt(src, rest); src[at.start:at.end] != "rest" {
		t.Errorf("name at %v", at)
	}
}

func syntaxPatterns(path []pattern) string {
	var out []string
	for _, p := range path {
		switch p.kind {
		case patternLiteral:
			out = append(out, p.text)
		case patternVariable:
			out = append(out, "{"+p.text+"}")
		case patternRest:
			out = append(out, "{"+p.text+"=**}")
		}
	}
	return strings.Join(out, " ")
}

// The name of a variable is found past comments and spaces.
func TestPatternNameAt(t *testing.T) {
	src := "service s { match / { /*x*/ x = * * } { allow read; } }"
	f := syntaxParse(t, src)
	pt := f.body.matches[0].path[0]
	if pt.kind != patternRest || pt.text != "x" {
		t.Fatalf("pattern %+v", pt)
	}
	if at := patternNameAt(src, pt); at.start != strings.LastIndex(src, "x =") {
		t.Errorf("name at %d", at.start)
	}
}

func TestParseSpans(t *testing.T) {
	src := "service s { match /a { allow read: if !request.auth.token['x'][0:1].size() + (b) * 2 in [c, d] ? e is int : f; } }"
	f := syntaxParse(t, src)
	cond := f.body.matches[0].body.allows[0].cond
	text := func(e expr) string { return src[e.pos().start:e.pos().end] }
	ce := cond.(*condExpr)
	if text(ce) != "!request.auth.token['x'][0:1].size() + (b) * 2 in [c, d] ? e is int : f" {
		t.Errorf("?: span %q", text(ce))
	}
	in := ce.test.(*binaryExpr)
	if in.op != tokIn || text(in.y) != "[c, d]" {
		t.Errorf("in %q", text(in))
	}
	plus := in.x.(*binaryExpr)
	call := plus.x.(*unaryExpr).x.(*callExpr)
	if text(call) != "request.auth.token['x'][0:1].size()" || text(call.fn) != "request.auth.token['x'][0:1].size" {
		t.Errorf("call %q, fn %q", text(call), text(call.fn))
	}
	if m := call.fn.(*memberExpr); src[m.nameAt.start:m.nameAt.end] != "size" {
		t.Errorf("name at %v", m.nameAt)
	}
	// Parentheses are left out of the tree: the expression keeps its own span.
	if b := plus.y.(*binaryExpr).x; text(b) != "b" {
		t.Errorf("parenthesized %q", text(b))
	}
	if is := ce.yes.(*isExpr); text(is) != "e is int" || src[is.typAt.start:is.typAt.end] != "int" {
		t.Errorf("is %q", text(is))
	}
}

// Wraps a declaration in a match, as the compiler's errors are measured in.
func syntaxInMatch(decl string) string {
	return "rules_version = '2';\nservice cloud.firestore {\n  match /databases/{database}/documents {\n    match /{document=**} {\n      " +
		decl + "\n    }\n  }\n}\n"
}

// Every error the compiler reports, at its position, as hidane recorded them on the emulator
// (tests/fixtures/rules_compile.json): ANTLR's recovery, the grammar's own errors and the
// lexer's. stops tells whether the file is then not checked further.
func TestParseErrors(t *testing.T) {
	for _, c := range []struct {
		name, src, want string
		stops           bool
	}{
		{"empty file", "", "L0:0 Rules content empty. Compilation aborted.", true},
		{"only a version", "rules_version = '2';\n", "L2:1 mismatched input '<EOF>' expecting {'function', 'import', 'service'}", true},
		{"nothing", "  \n", "L2:1 mismatched input '<EOF>' expecting {'function', 'import', 'service', 'rules_version'}", true},
		{"version as a number", "rules_version = 2;\nservice s { match /a { allow read; } }", "L1:17 mismatched input '2' expecting STRING", true},
		{"version without equals", "rules_version \"2\";\nservice s { match /a { allow read; } }", `L1:15 missing '=' at '"2"'`, true},
		{"empty service", "rules_version = '2';\nservice cloud.firestore {\n}\n", "L3:1 The 'service' body must contain at least one declaration.", false},
		{"empty match", "service cloud.firestore {\n  match /databases/{database}/documents {\n\n  }\n}\n", "L4:3 The 'match' body must contain at least one declaration.", false},
		{"trailing text", syntaxInMatch("allow read;") + "garbage\n", "L9:1 Unexpected 'garbage'.", true},
		{"missing closing brace", strings.TrimSuffix(syntaxInMatch("allow read;"), "}\n") + "\n", "L9:1 Unexpected '<EOF>'.\nL9:1 missing '}' at '<EOF>'", true},
		{"extra closing brace", syntaxInMatch("allow read;") + "}\n", "L9:1 Unexpected '}'.", true},
		{"allow outside a match", "rules_version = '2';\nservice cloud.firestore {\n  allow read: if true;\n}\n", "L3:3 Unexpected 'allow'.", true},
		{"import", "rules_version = '2';\nimport \"x.rules\";\nservice s {\n  match /a {\n    allow read;\n  }\n}\n",
			"L2:1 Import statements are not allowed.\nL2:1 Module \"x.rules\" is not part of the standard library.", false},
		{"duplicate service", "service a.b { match /x { allow read; } }\nservice a.b { match /y { allow read; } }\n", "L2:9 Duplicate service name declaration.", false},
		{"allow without if", syntaxInMatch("allow read: true;"), "L5:19 Missing 'if' before conditional expression.", false},
		{"allow with empty condition", syntaxInMatch("allow read: if ;"), "L5:22 Missing conditional expression after 'if'.", false},
		{"allow without colon", syntaxInMatch("allow read if true;"), "L5:18 Missing ':' before 'if' condition.", false},
		{"allow without methods", syntaxInMatch("allow : if true;"), "L5:13 Expected at least one 'operation' after 'allow'.\nL5:13 Unexpected ':'.", true},
		{"allow with a trailing comma", syntaxInMatch("allow read, : if true;"), "L5:17 Unexpected ','.", true},
		{"allow without a comma", syntaxInMatch("allow read write: if true;"), "L5:18 Unexpected 'write'.", true},
		{"allow with only a colon", syntaxInMatch("allow read: ;"), "L5:19 Unexpected ';'.", true},
		{"operator without operand", syntaxInMatch("allow read: if true &&;"),
			"L5:29 mismatched input ';' expecting {'--', '++', '{', '[', '(', '-', '!', '/', 'false', 'null', 'true', PATH_SEGMENT, IDENTIFIER, NUM_INT, NUM_FLOAT, UNPAIRED_DOUBLE_QUOTE, UNPAIRED_SINGLE_QUOTE, STRING, BYTES}", true},
		{"placeholder condition", syntaxInMatch("allow read: if <condition>;"), "L5:22 Missing conditional expression after 'if'.\nL5:22 Unexpected '<'.", true},
		{"missing operand in a call", syntaxInMatch("allow read: if f(,);"), "L5:24 Unexpected ','.", true},
		{"missing map value", syntaxInMatch("allow read: if {'a': };"), "L5:28 Unexpected '}'.\nL5:29 Unexpected ';'.\nL8:1 Unexpected '}'.", true},
		{"missing ternary colon", syntaxInMatch("allow read: if true ? 'a' 'b';"), "L5:33 missing ':' at ''b''", true},
		{"unclosed parenthesis", syntaxInMatch("allow read: if (true;"), "L5:27 Unexpected ';'.", true},
		{"unclosed string", syntaxInMatch("allow read: if 'abc;"), "L6:5 Missing a closing '", false},
		{"unclosed string in parentheses", syntaxInMatch(`allow read: if ("abc;`), "L6:5 Unexpected '}'.", true},
		{"decrement", syntaxInMatch("allow read: if --1 == -1;"), "L5:24 Increment (++) and decrement (--) operators are not supported.", false},
		{"decrement twice", syntaxInMatch("allow read: if ----1 == 1;"), "L5:24 Increment (++) and decrement (--) operators are not supported.\nL5:24 Unexpected '--'.", true},
		{"negated negation", syntaxInMatch("allow read: if - -1 == 1;"), "L5:24 mismatched input '-' expecting {'{', '[', '(', '/', 'false', 'null', 'true', PATH_SEGMENT, IDENTIFIER, NUM_INT, NUM_FLOAT, UNPAIRED_DOUBLE_QUOTE, UNPAIRED_SINGLE_QUOTE, STRING, BYTES}", true},
		{"is with an unknown type", syntaxInMatch("allow read: if 1 is foo;"),
			"L5:30 An unsupported type identifier was used with the 'is' operator. Received foo. Expected one of bool,bytes,constraint,duration,float,int,latlng,list,vector,set,number,map,string,timestamp,path,map_diff", false},
		{"reserved word as a method", syntaxInMatch("allow read: if request.x.while() == 1;"), "L5:37 Unexpected '('.", true},
		{"reserved word as a variable", syntaxInMatch("allow read: if var == 1;"), "L5:22 Missing conditional expression after 'if'.\nL5:22 Unexpected 'var'.", true},
		{"rules_version as a field", syntaxInMatch("allow read: if a.rules_version;"), "L5:24 Unexpected 'rules_version'.", true},
		{"at sign", syntaxInMatch("allow read: if @;"), "L5:22 Missing conditional expression after 'if'.\nL5:22 Unexpected '@'.", true},
		// 4/2 is the number 4 and the path segment /2: a path where a declaration may start.
		{"division without spaces", syntaxInMatch("allow read: if 4/2 == 2;"), "L5:23 Missing 'match' keyword before path.\nL5:26 mismatched input '==' expecting {'{', '/', PATH_SEGMENT}", true},
		{"exponent", syntaxInMatch("allow read: if 1e3 == 1000;"), "L5:23 Unexpected 'e3'.", true},
		{"exponent of a float", syntaxInMatch("allow read: if 2.5e-3 > 0;"), "L5:25 Unexpected 'e'.", true},
		{"hex", syntaxInMatch("allow read: if 0x10 == 16;"), "L5:23 Unexpected 'x10'.", true},
		{"raw string", syntaxInMatch("allow read: if r'a' == 'a';"), "L5:23 Unexpected ''a''.", true},
		{"match without slash", syntaxInMatch("match {document=**} {\n allow read;\n }"), "L5:13 Unexpected '{'.\nL5:27 Unexpected '{'.\nL9:3 Unexpected '}'.", true},
		{"path keyword missing", syntaxInMatch("/foo/{bar} {\n allow read;\n }"), "L5:7 Missing 'match' keyword before path.\nL6:2 missing '}' at 'allow'\nL10:1 Unexpected '}'.", true},
		{"trailing slash", syntaxInMatch("match /a/ {\n allow read;\n }"), "L5:15 Forward slash '/' found where identifier or binding expected.", false},
		{"empty segment", syntaxInMatch("match /a//b {\n allow read;\n }"), "L5:13 Missing 'match' keyword before path.\nL6:2 Unexpected 'allow'.\nL6:2 mismatched input 'allow' expecting {'{', '/', PATH_SEGMENT}\nL10:1 Unexpected '}'.", true},
		{"match path then semicolon", syntaxInMatch("match /a ;"), "L5:13 Missing 'match' keyword before path.\nL5:16 Unexpected ';'.\nL5:16 mismatched input ';' expecting {'{', '/', PATH_SEGMENT}", true},
		{"match path then brace", syntaxInMatch("match /a }"), "L5:13 Missing 'match' keyword before path.\nL5:16 Unexpected '}'.\nL5:16 missing '{' at '}'", true},
		{"match with one stray word", syntaxInMatch("match /a {\n foo\n }"), "L5:13 Missing 'match' keyword before path.\nL6:2 Unexpected 'foo'.\nL6:2 Unexpected 'foo'.", true},
		{"function without return", syntaxInMatch("function f() {\n true;\n }"), "L6:2 Unexpected 'true'.\nL10:1 Unexpected '}'.", true},
		{"let after return", syntaxInMatch("function f() {\n return true;\n let x = 1;\n }"), "L7:2 mismatched input 'let' expecting '}'\nL11:1 Unexpected '}'.", true},
		{"two returns", syntaxInMatch("function f() {\n return true;\n return false;\n }"), "L7:2 mismatched input 'return' expecting '}'\nL11:1 Unexpected '}'.", true},
		{"function without a body", "service s {\n  function f() {\n  }\n  match /a { allow read; }\n}\n", "L3:3 Missing 'function' body", false},
		{"function without a name", "service s {\n  function () {\n    return true;\n  }\n  match /a { allow read; }\n}\n", "L2:12 Missing 'function' id", false},
		{"function without parameters", "service s {\n  function f {\n    return true;\n  }\n  match /a { allow read; }\n}\n", "L2:14 Missing 'function' signature", false},
		{"function with only a let", "service s {\n  function f() {\n    let x = 1;\n  }\n  match /a { allow read; }\n}\n",
			"L4:3 Unexpected '}'.\nL4:3 mismatched input '}' expecting {'&&', '.', '==', '>', '>=', '[', '<', '<=', '-', '%', '!=', '||', '+', ';', '/', '*', '?', 'in', 'is'}", true},
		{"let without a semicolon", syntaxInMatch("function f() {\n let x = 1\n return x == 1;\n }"), "L7:2 missing ';' at 'return'", true},
		// The lexer's errors; one before the first syntax error is not reported.
		{"non-breaking space", syntaxInMatch("\u00a0allow read;"), "L5:7 token recognition error at: '\u00a0'", false},
		{"characters that start no token", syntaxInMatch("allow read: if true # ~;"), "L5:27 token recognition error at: '#'\nL5:29 token recognition error at: '~'", false},
		{"lexer error before a syntax error", syntaxInMatch("allow read: if '日本' # &&;"),
			"L5:31 mismatched input ';' expecting {'--', '++', '{', '[', '(', '-', '!', '/', 'false', 'null', 'true', PATH_SEGMENT, IDENTIFIER, NUM_INT, NUM_FLOAT, UNPAIRED_DOUBLE_QUOTE, UNPAIRED_SINGLE_QUOTE, STRING, BYTES}", true},
		{"lexer error after a syntax error", syntaxInMatch("allow read: if true &&;") + "\u00a0\n",
			"L5:29 mismatched input ';' expecting {'--', '++', '{', '[', '(', '-', '!', '/', 'false', 'null', 'true', PATH_SEGMENT, IDENTIFIER, NUM_INT, NUM_FLOAT, UNPAIRED_DOUBLE_QUOTE, UNPAIRED_SINGLE_QUOTE, STRING, BYTES}\nL9:1 token recognition error at: '\u00a0'", true},
		{"identifier outside ASCII", syntaxInMatch("allow read: if 日本 == 1;"), "L5:25 Missing conditional expression after 'if'.\nL5:25 Unexpected '=='.", true},
		// Columns count code points: Arabic and an emoji before an error.
		{"text before an error", syntaxInMatch("allow read: if 'نص 🙂' &&;"),
			"L5:31 mismatched input ';' expecting {'--', '++', '{', '[', '(', '-', '!', '/', 'false', 'null', 'true', PATH_SEGMENT, IDENTIFIER, NUM_INT, NUM_FLOAT, UNPAIRED_DOUBLE_QUOTE, UNPAIRED_SINGLE_QUOTE, STRING, BYTES}", true},
		{"comment before an error", syntaxInMatch("allow read: /* نص 🙂 */ true;"), "L5:30 Missing 'if' before conditional expression.", false},
		{"CRLF", strings.ReplaceAll(syntaxInMatch("allow read: true;"), "\n", "\r\n"), "L5:19 Missing 'if' before conditional expression.", false},
		{"numbers out of range", syntaxInMatch("allow read: if 9223372036854775808 + -(9223372036854775808) + 9223372036854775807 + -9223372036854775808 == 0;"),
			"L5:22 Invalid number literal 9223372036854775808.\nL5:46 Invalid number literal 9223372036854775808.", false},
	} {
		f, issues := parse("t.rules", c.src, DefaultLimits())
		if got := syntaxIssues(issues); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
		if (f == nil) != c.stops {
			t.Errorf("%s: file %v, want stops %v", c.name, f != nil, c.stops)
		}
		for _, i := range issues {
			if i.Severity != SeverityError || i.File != "t.rules" {
				t.Errorf("%s: issue %+v", c.name, i)
			}
		}
	}
}

// A missing condition is false, never no condition, which would grant.
func TestParseMissingConditionDenies(t *testing.T) {
	f, issues := parse("t.rules", syntaxInMatch("allow read: if ;"), DefaultLimits())
	if f == nil || len(issues) != 1 {
		t.Fatalf("%v", syntaxIssues(issues))
	}
	cond := f.body.matches[0].body.matches[0].body.allows[0].cond
	if cond == nil || syntaxShape(cond) != "false" {
		t.Errorf("condition %v", cond)
	}
}

// Two services of different names compile: the tree keeps the first.
func TestParseTwoServices(t *testing.T) {
	f := syntaxParse(t, "service firebase.storage { match /b/{bucket}/o { allow read; } }\nservice cloud.firestore { match /a { allow read; } }\n")
	if f.service != "firebase.storage" || len(f.body.matches) != 1 {
		t.Errorf("service %q", f.service)
	}
}

// syntaxRepeat builds a condition from n copies of a part around a core.
func syntaxRepeat(before string, n int, core, after string) string {
	return strings.Repeat(before, n) + core + strings.Repeat(after, n)
}

// syntaxTernaries is the production oracle's shape: n conditionals, each in parentheses and
// nested in the branch taken, "(1 == 1 ? (1 == 1 ? true : false) : false)" for 2.
func syntaxTernaries(n int) string {
	return syntaxRepeat("(1 == 1 ? ", n, "true", " : false)")
}

// Nesting 99 deep is too complex; 98 is not (ADR-021 §4). Each boundary is the oracle's: the
// production oracle for && chains, parentheses and conditionals in parentheses; the emulator,
// as hidane recorded it, for the others. The error is at every node at depth 99.
func TestParseNesting(t *testing.T) {
	for _, c := range []struct {
		name, cond string
		errors     []int // the columns of the errors, on line 5
	}{
		{"&& of 99 terms", strings.Repeat("true && ", 98) + "true", nil},
		{"&& of 100 terms", strings.Repeat("true && ", 99) + "true", []int{22, 30}},
		{"parentheses 98 deep", syntaxRepeat("(", 98, "true", ")"), nil},
		{"parentheses 99 deep", syntaxRepeat("(", 99, "true", ")"), []int{22 + 99}},
		{"48 conditionals in parentheses", syntaxTernaries(48), nil},
		{"49 conditionals in parentheses", syntaxTernaries(49), []int{22 + 48*10 + 1, 22 + 48*10 + 6}},
		{"negations 98 deep", strings.Repeat("!", 98) + "true", nil},
		{"negations 99 deep", strings.Repeat("!", 99) + "true", []int{22 + 99}},
		{"lists 98 deep", syntaxRepeat("[", 98, "", "]") + " == []", nil},
		{"lists 99 deep", syntaxRepeat("[", 99, "", "]") + " == []", []int{22 + 98}},
		{"maps 97 deep", syntaxRepeat("{'a': ", 97, "1", "}") + " != null", nil},
		{"maps 98 deep", syntaxRepeat("{'a': ", 98, "1", "}") + " != null", []int{22 + 97*6 + 1, 22 + 97*6 + 6}},
		// -(x) is one level: under ==, the 1 of 98 is at depth 99, and the 99th - is.
		{"minus 97 deep", syntaxRepeat("-(", 97, "1", ")") + " == 1", nil},
		{"minus 98 deep", syntaxRepeat("-(", 98, "1", ")") + " == 1", []int{22 + 2*98}},
		{"minus 99 deep", syntaxRepeat("-(", 99, "1", ")") + " == 1", []int{22 + 2*98}},
		{"conditionals 98 deep", syntaxRepeat("true ? ", 98, "true", " : false"), nil},
		{"conditionals 99 deep", syntaxRepeat("true ? ", 99, "true", " : false"), []int{22 + 98*7, 22 + 99*7, 22 + 99*7 + 7}},
		{"calls 99 deep", syntaxRepeat("f(", 99, "1", ")"), []int{22 + 2*99}},
		{"too complex and an unknown function", syntaxRepeat("(", 100, "true", ")") + " && foo()", []int{22 + 98}},
		// Member accesses do not count.
		{"members 600 long", "request" + strings.Repeat(".a", 600) + " == 1", nil},
		{"methods 600 long", "request" + strings.Repeat(".a()", 600) + " == 1", nil},
	} {
		f, issues := parse("t.rules", syntaxInMatch("allow read: if "+c.cond+";"), DefaultLimits())
		var want []string
		for _, col := range c.errors {
			want = append(want, fmt.Sprintf("L5:%d %s", col, msgTooComplex))
		}
		if got := syntaxIssues(issues); got != strings.Join(want, "\n") {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, want)
		}
		if f == nil {
			t.Errorf("%s: too complex stops the compiler", c.name)
		}
	}
}

// The depth is the configured limit's, and the member chain is Aruma's limit.
func TestParseLimits(t *testing.T) {
	lim := DefaultLimits()
	lim.ExpressionDepth = 3
	lim.MemberChain = 5
	for _, c := range []struct{ cond, want string }{
		{"(((true)))", ""},
		{"((((true))))", "L5:26 " + msgTooComplex},
		{"a.b.c.d[0].f() == 1", ""},
		{"a.b.c.d[0].f().g == 1", "L5:36 " + msgTooComplex},
	} {
		_, issues := parse("t.rules", syntaxInMatch("allow read: if "+c.cond+";"), lim)
		if got := syntaxIssues(issues); got != c.want {
			t.Errorf("%s: %q, want %q", c.cond, got, c.want)
		}
	}
	// The default chain is 2,000.
	_, issues := parse("t.rules", syntaxInMatch("allow read: if a"+strings.Repeat(".a", 2001)+";"), DefaultLimits())
	if got := syntaxIssues(issues); got != "L5:4023 "+msgTooComplex {
		t.Errorf("a chain of 2,001: %q", got)
	}
}

// What nests far deeper than the compiler allows is refused quickly, without exhausting the
// stack.
func TestParseDeepInputIsBounded(t *testing.T) {
	for _, cond := range []string{
		syntaxRepeat("(", 100_000, "true", ")"),
		syntaxRepeat("[", 100_000, "", "]"),
		strings.Repeat("!", 100_000) + "true",
		strings.Repeat("a && ", 120_000) + "a",
		syntaxRepeat("f(", 50_000, "", ")"),
		syntaxRepeat("a[", 50_000, "0", "]"),
		strings.Repeat("true ? ", 50_000) + "true" + strings.Repeat(" : false", 50_000),
		"a" + strings.Repeat(".a", 200_000),
	} {
		start := time.Now()
		_, issues := parse("t.rules", syntaxInMatch("allow read: if "+cond+";"), DefaultLimits())
		if len(issues) == 0 || !strings.Contains(syntaxIssues(issues), msgTooComplex) {
			t.Errorf("%.20s…: %.300s", cond, syntaxIssues(issues))
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("%.20s…: %v", cond, d)
		}
	}
	src := strings.Repeat("match /a {\n", 5000)
	start := time.Now()
	f, _ := parse("t.rules", "service s {\n"+src+"allow read;\n"+strings.Repeat("}\n", 5001), DefaultLimits())
	if f == nil || time.Since(start) > 5*time.Second {
		t.Errorf("match blocks 5,000 deep: %v in %v", f != nil, time.Since(start))
	}
}

// FuzzSyntax: the parser never panics, places every issue and node inside the text, gives the
// same answer twice, and returns in bounded time. (The package's FuzzParse, which seeds from
// the oracle's cases, is S1-D's.)
func FuzzSyntax(f *testing.F) {
	for _, seed := range []string{
		syntaxInMatch("allow read: if request.auth != null && get(/databases/$(database)/documents/u/$(request.auth.uid)).data.admin == true;"),
		syntaxInMatch("function f(a, b) { let c = a.size() > 0 ? a[1:2] : b; return c is string || 4/2 == 2; }"),
		syntaxInMatch("allow read: if {'a': [1, 2.5, b'\\x01', \"s\\u00e9\"]}.a[0] in [1] && !(-x % 2 == 1);"),
		"rules_version = '1';\nfunction g() { return true; }\nservice firebase.storage { match /b/{bucket}/o/{p=**} { allow read, write: if g(); } }",
		syntaxInMatch("allow read: if ((((((((true))))))));"),
		syntaxInMatch("match /a//b {"),
		"\u00a0'\"/**/ /*",
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 1<<16 {
			return
		}
		start := time.Now()
		file, issues := parse("f.rules", src, DefaultLimits())
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("parse took %v", d)
		}
		_, again := parse("f.rules", src, DefaultLimits())
		if syntaxIssues(issues) != syntaxIssues(again) {
			t.Fatal("two parses differ")
		}
		points := len([]rune(src))
		for _, i := range issues {
			p := i.Position
			if p == nil {
				if src != "" {
					t.Fatalf("an issue without a position: %v", i)
				}
				continue
			}
			if p.Line < 1 || p.Column < 1 || p.CurrentOffset < 0 || p.CurrentOffset > points ||
				p.EndOffset < p.CurrentOffset || p.EndOffset > max(points, 1) {
				t.Fatalf("issue %v outside %d code points", i, points)
			}
		}
		if file != nil {
			syntaxEachExpr(file, func(e expr) {
				if s := e.pos(); s.start < 0 || s.end < s.start || s.end > len(src) {
					t.Fatalf("%T at %v outside %d bytes", e, s, len(src))
				}
			})
		}
	})
}

// syntaxEachExpr calls fn on every expression of the file, without recursion.
func syntaxEachExpr(f *file, fn func(expr)) {
	var stack []expr
	var blocks func(b block)
	funcs := func(fs []*function) {
		for _, x := range fs {
			stack = append(stack, x.ret)
			for _, l := range x.lets {
				stack = append(stack, l.value)
			}
		}
	}
	blocks = func(b block) {
		funcs(b.functions)
		for _, a := range b.allows {
			if a.cond != nil {
				stack = append(stack, a.cond)
			}
		}
		for _, m := range b.matches {
			blocks(m.body)
		}
	}
	funcs(f.functions)
	blocks(f.body)
	for len(stack) > 0 {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if e == nil {
			continue
		}
		fn(e)
		switch e := e.(type) {
		case *memberExpr:
			stack = append(stack, e.x)
		case *indexExpr:
			stack = append(stack, e.x, e.i)
		case *sliceExpr:
			stack = append(stack, e.x, e.lo, e.hi)
		case *callExpr:
			stack = append(stack, e.fn)
			stack = append(stack, e.args...)
		case *unaryExpr:
			stack = append(stack, e.x)
		case *binaryExpr:
			stack = append(stack, e.x, e.y)
		case *isExpr:
			stack = append(stack, e.x)
		case *condExpr:
			stack = append(stack, e.test, e.yes, e.no)
		case *listExpr:
			stack = append(stack, e.elems...)
		case *mapExpr:
			stack = append(stack, e.keys...)
			stack = append(stack, e.vals...)
		case *pathExpr:
			for _, s := range e.segs {
				stack = append(stack, s.x)
			}
		}
	}
}
