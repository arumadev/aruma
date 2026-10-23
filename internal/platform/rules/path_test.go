package rules

import (
	"testing"
)

func TestBuildPath(t *testing.T) {
	text := func(s string) pathPart { return pathPart{text: s} }
	val := func(v Value) pathPart { return pathPart{value: v, isVal: true} }
	build := func(parts ...pathPart) testExpr {
		return func(m *meter) (Value, error) { return buildPath(m, parts) }
	}
	runValueCases(t, []valueCase{
		// /databases/$(database)/documents/users/$(uid): Pyric path-constructor-and-bind.
		{"a document path", build(text("databases"), val(vs("(default)")), text("documents"), text("users"), val(vs("alice"))),
			vp("databases", "(default)", "documents", "users", "alice"), ""},
		// Literal segments are URL-decoded as Java's URLDecoder does, %XX and + (ADR-021 §5.7).
		{"%20 and +", build(text("a%20b"), text("c+d")), vp("a b", "c d"), ""},
		{"%2F is one segment", build(text("a%2Fb")), vp("a/b"), ""},
		{"UTF-8 escapes", build(text("%C3%A9")), vp("é"), ""},
		{"a lone %", build(text("100%"), text("%zz")), vp("100%", "%zz"), ""},
		// A $(x) string is not decoded: it is one segment as it is.
		{"$(x) is not decoded", build(val(vs("a%20b/c"))), vp("a%20b/c"), ""},
		// A $(p) path inserts all of its segments (hidane interp.rs, library.rs).
		{"$(p) inserts a path", build(text("databases"), val(vp("x", "y")), text("z")), vp("databases", "x", "y", "z"), ""},
		{"$(1) as string() writes it", build(text("a"), val(vi(1)), val(vb(true))), vp("a", "1", "true"), ""},
		{"$(list) is an error", build(text("a"), val(vl())), Value{}, "Received: string(list)."},
	})
}

func TestPathFunction(t *testing.T) {
	runValueCases(t, []valueCase{
		// Pyric path-constructor-and-bind, conversion-functions (production).
		{"path('users/alice')", vFn("path", vs("users/alice")), vp("users", "alice"), ""},
		{"a leading slash", vFn("path", vs("/a/b")), vp("a", "b"), ""},
		{"decoded", vFn("path", vs("a+b/%41")), vp("a b", "A"), ""},
		{"bind of an empty map", vThen(vFn("path", vs("a/b")), "bind", vm()), vp("a", "b"), ""},
		{"path(path)", vFn("path", vp("a")), Value{}, "Unsupported operation error. Received: path(path). Expected: path(string)."},
		{"an empty path", vFn("path", vs("")), Value{}, "Empty Path Template is not allowed."},
		{"an empty segment", vFn("path", vs("a//b")), Value{}, "Invalid Path Template a//b. Empty segment is not allowed."},
		{"a trailing slash", vFn("path", vs("a/")), Value{}, "Empty segment is not allowed."},
		{"path('users/alice')[1]", vOpOn(vFn("path", vs("users/alice")), tokEQ, vLit(vp("users", "alice"))), vb(true), ""},
		// bind fills template variables (Pyric, production; hidane says paths built in rules
		// have none to bind, and production wins).
		{"bind", vThen(vFn("path", vs("users/{uid}")), "bind", vm("uid", vs("alice"))), vp("users", "alice"), ""},
		{"bind two", vCall(vp("a", "{x}", "{y}"), "bind", vm("x", vs("1"), "y", vi(2))), vp("a", "1", "2"), ""},
		{"bind a path into {rest=**}", vCall(vp("a", "{rest=**}"), "bind", vm("rest", vp("b", "c"))), vp("a", "b", "c"), ""},
		{"bind nothing", vCall(vp("a", "{x}"), "bind", vm()), vp("a", "{x}"), ""},
		{"bind a missing variable", vCall(vp("a", "{x}"), "bind", vm("x", vs("1"), "z", vs("2"), "q", vs("3"))), Value{}, "Variable(s) q, z do not exist in the path template."},
		{"bind a list", vCall(vp("{x}"), "bind", vm("x", vl())), Value{}, "Received: string(list)."},
		{"bind a string", vCall(vp("a"), "bind", vs("a")), Value{}, "Received: path.bind(string). Expected: path.bind(map)."},
		{"no such method", vCall(vp("a"), "size"), Value{}, "Name: [size]."},
	})
	for seg, want := range map[string]string{"{x}": "x", "{rest=**}": "rest", "{}": "", "x": "", "{a=b}": "", "{=**}": ""} {
		name, _, ok := templateVar(seg)
		if ok != (want != "") || name != want && ok {
			t.Errorf("templateVar(%q) = %q, %v; want %q", seg, name, ok, want)
		}
	}
}
