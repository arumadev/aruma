package rules

import (
	"fmt"
	"strings"
	"testing"
)

// syntaxLexemes prints the tokens of src as kind:text, without the end.
func syntaxLexemes(src string) string {
	toks, _ := lex(src)
	names := map[lexKind]string{lexOp: "op", lexIdent: "id", lexInt: "int", lexFloat: "float",
		lexString: "str", lexBytes: "bytes", lexUnpaired: "unpaired", lexSegment: "seg", lexDollar: "$", lexAt: "@"}
	var out []string
	for _, t := range toks {
		if t.kind == lexEOF {
			if t.start != len(src) || t.end != len(src) {
				return fmt.Sprintf("EOF at %v", t.span)
			}
			continue
		}
		out = append(out, names[t.kind]+":"+src[t.start:t.end])
	}
	return strings.Join(out, " ")
}

// The lexical rules hidane measured on the emulator (lexer.rs, #59 and #61).
func TestLexTokens(t *testing.T) {
	for _, c := range []struct{ src, want string }{
		// A path segment is one token, even after a number: 4/2 is 4 and /2, 4 / 2 divides.
		{"4/2", "int:4 seg:/2"},
		{"4 / 2", "int:4 op:/ int:2"},
		{"a/b", "id:a seg:/b"},
		{"/databases/$(db)/(default)", "seg:/databases op:/ $:$ op:( id:db op:) seg:/(default)"},
		{"/a%20b:c@d", "seg:/a%20b:c@d"},
		{"/a+b/-j/.k/~l/&m/'n'/*o*", "seg:/a+b seg:/-j seg:/.k seg:/~l seg:/&m seg:/'n' seg:/*o*"},
		{"/a%b", "seg:/a op:% id:b"},
		{"/a(b)", "seg:/a op:( id:b op:)"},
		{"/(a", "op:/ op:( id:a"},
		{"/()", "op:/ op:( op:)"},
		{"/{x=**}", "op:/ op:{ id:x op:= op:* op:* op:}"},
		// Comments; a comment wins when it is at least as long as a segment.
		{"a // b\nc", "id:a id:c"},
		{"a /* b\n */ c", "id:a id:c"},
		{"a /**/ c", "id:a id:c"},
		{"/*a", "seg:/*a"},
		{"/a//b {", "seg:/a"},
		{"x // end", "id:x"},
		// Numbers are decimal, with no exponent and no hex.
		{"1 1.5 .5 1. 1..2", "int:1 float:1.5 float:.5 float:1. float:1. float:.2"},
		{"a.1", "id:a float:.1"},
		{"1e3", "int:1 id:e3"},
		{"0x1F", "int:0 id:x1F"},
		{"007", "int:007"},
		// Identifiers may hold $; a lone $ and @ are tokens of their own.
		{"$b a$ $ ( @", "id:$b id:a$ $:$ op:( @:@"},
		{"_x1 if is in", "id:_x1 id:if id:is id:in"},
		// Strings, both quotes, escapes, bytes; an unclosed string stops where it cannot go on.
		{`'a\'b' "c\"d"`, `str:'a\'b' str:"c\"d"`},
		{`'\n\r\t\b\f\\\x41é\101'`, `str:'\n\r\t\b\f\\\x41é\101'`},
		{"b'ab' B\"cd\"", "bytes:b'ab' bytes:B\"cd\""},
		{"b'ab", "id:b unpaired:'ab"},
		{"bx'a'", "id:bx str:'a'"},
		{"'ab", "unpaired:'ab"},
		{"\"a\nb\"", "unpaired:\"a id:b unpaired:\""},
		{"'a\rb'", "unpaired:'a id:b unpaired:'"},
		{`'\a'`, `unpaired:' id:a unpaired:'`},
		{`'\x4'`, `unpaired:' id:x4 unpaired:'`},
		{`'\u00e'`, `unpaired:' id:u00e unpaired:'`},
		{`'\400'`, `unpaired:' int:400 unpaired:'`},
		{"r'raw'", "id:r str:'raw'"},
		{"'''a'''", "str:'' str:'a' str:''"},
		// Operators, longest first; ++ and -- are tokens.
		{"--x ++ && || == != <= >= < > = ! + - * % ? : ; , .", "op:-- id:x op:++ op:&& op:|| op:== op:!= op:<= op:>= op:< op:> op:= op:! op:+ op:- op:* op:% op:? op:: op:; op:, op:."},
		{"a---b", "id:a op:-- op:- id:b"},
		{"\t\f\r\n", ""},
		{"", ""},
	} {
		if got := syntaxLexemes(c.src); got != c.want {
			t.Errorf("lex(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

// A character that starts no token is a "token recognition error" and is dropped; a lone & or
// | takes the character after it.
func TestLexErrors(t *testing.T) {
	src := "a b\né &&& c # ~ \\ `|"
	toks, errs := lex(src)
	var got []string
	for _, e := range errs {
		got = append(got, fmt.Sprintf("%d-%d %s", e.start, e.end, e.text))
	}
	want := []string{"1-3  ", "5-7 é", "10-12 & ", "14-15 #", "16-17 ~", "18-19 \\", "20-21 `", "21-22 |"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("errors = %q, want %q", got, want)
	}
	if got := syntaxLexemes(src); got != "id:a id:b op:&& id:c" {
		t.Errorf("tokens = %q", got)
	}
	if len(toks) == 0 || toks[len(toks)-1].kind != lexEOF {
		t.Error("no EOF token")
	}
	_, errs = lex("a &\n")
	if len(errs) != 1 || errs[0].text != `&\n` {
		t.Errorf("a lone & before a line break: %+v", errs)
	}
}

func TestLexUnquote(t *testing.T) {
	for _, c := range []struct{ lit, want string }{
		{`'abc'`, "abc"},
		{`"it's"`, "it's"},
		{`'a\n\x41é\101'`, "a\nAéA"},
		{`'\r\t\b\f\\\'\"'`, "\r\t\b\f\\'\""},
		{`'\xe9\u00e9\351'`, "ééé"},
		{`'\u0041'`, "A"},
		// A surrogate pair is one code point, as the compiler reads UTF-16; a lone one is U+FFFD.
		{`'\ud83d\ude00'`, "😀"},
		{`'\ud83dx'`, "\uFFFDx"},
		{`'\ude00'`, "\uFFFD"},
		{`'نص 🙂'`, "نص 🙂"},
		// Bytes take one byte for \x and octal escapes, and the low byte of a \u escape.
		{`b'\xff\000a'`, "\xff\x00a"},
		{`b'é'`, "é"},
		{`B"\u0141"`, "\x41"},
	} {
		if got := lexUnquote(c.lit); got != c.want {
			t.Errorf("lexUnquote(%s) = %q, want %q", c.lit, got, c.want)
		}
	}
}
