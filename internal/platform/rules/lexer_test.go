package rules

import (
	"fmt"
	"strings"
	"testing"
	"time"
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
		// Strings are WTF-8: a surrogate pair is one code point, as the compiler reads UTF-16, and a
		// lone surrogate keeps its own three bytes, never U+FFFD (production).
		{`'\ud83d\ude00'`, "😀"},
		{`'\ud83dx'`, "\xed\xa0\xbdx"},
		{`'\ude00'`, "\xed\xb8\x80"},
		{`'\ud83d\ud83d\ude00'`, "\xed\xa0\xbd😀"},
		{`'\ude00\ud83d'`, "\xed\xb8\x80\xed\xa0\xbd"},
		{`'\uFFFD'`, "\uFFFD"},
		{`'نص 🙂'`, "نص 🙂"},
		// Bytes take one byte for \x and octal escapes; \u is refused by the parser.
		{`b'\xff\000a'`, "\xff\x00a"},
		{`b'é'`, "é"},
		{`B"\u0141"`, "\x41"},
	} {
		if got := lexUnquote(c.lit); got != c.want {
			t.Errorf("lexUnquote(%s) = %q, want %q", c.lit, got, c.want)
		}
	}
}

// The lone surrogate is WTF-8's form, distinct from U+FFFD, for every surrogate.
func TestLexAppendSurrogate(t *testing.T) {
	for r := rune(0xD800); r <= 0xDFFF; r++ {
		var b strings.Builder
		lexAppendSurrogate(&b, r)
		got := b.String()
		if len(got) != 3 || got[0] != 0xED || got[1] < 0xA0 || got[1] > 0xBF || got[2] < 0x80 || got[2] > 0xBF {
			t.Fatalf("%U: % x", r, got)
		}
	}
}

func TestLexBytesHasU(t *testing.T) {
	for lit, want := range map[string]bool{
		`b'` + "\\" + `u00ff'`: true, `B"` + "\\" + `u0041"`: true, `b'` + "\\\\" + `u0041'`: false,
		`b'` + "\\" + `xff'`: false, `b'u'`: false, `b'` + "\\" + `'u'`: false,
	} {
		if got := lexBytesHasU(lit); got != want {
			t.Errorf("lexBytesHasU(%s) = %v", lit, got)
		}
	}
}

// Unclosed block comments do not make the lexer quadratic (S1-A review: 256 KiB took 4 s).
func TestLexUnclosedCommentsAreLinear(t *testing.T) {
	for _, src := range []string{
		strings.Repeat("/*a", 1<<20/3) + "true",
		strings.Repeat("/*a", 1<<19/3) + "*/" + strings.Repeat("/*a", 1<<19/3),
		strings.Repeat("/**/", 1<<18),
	} {
		start := time.Now()
		toks, _ := lex(src)
		if d := time.Since(start); d > time.Second {
			t.Errorf("%.12s…: %v for %d tokens", src, d, len(toks))
		}
	}
	// The memo answers as a fresh search would.
	for _, c := range []struct{ src, want string }{
		{"/*a /*b */ c /*d", "id:c seg:/*d"},
		{"/*a*/ /*/ x */ y", "id:y"},
		{"/*a */ /*b", "seg:/*b"},
	} {
		if got := syntaxLexemes(c.src); got != c.want {
			t.Errorf("lex(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}
