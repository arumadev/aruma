package core

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHasEmptySegment(t *testing.T) {
	for name, want := range map[string]bool{
		"a": false, "a/b": false, "a b/c.png": false, "..": false,
		"": true, "/x": true, "x/": true, "a//b": true, "/": true, "a/b/": true,
	} {
		if got := hasEmptySegment(name); got != want {
			t.Errorf("hasEmptySegment(%q) = %v", name, got)
		}
	}
}

// pastPrefix is above every name that starts with the prefix, and no valid name lies between
// them that does not.
func TestPastPrefix(t *testing.T) {
	for prefix, want := range map[string]string{
		"a/":                     "a0",
		"photos/":                "photos0",
		"a":                      "b",
		"\x7f":                   "\u0080",
		"\u00e9":                 "\u00ea",
		"a\ud7ff":                "a\ue000",
		"a\U0010FFFF":            "b",
		"ab\U0010FFFF\U0010FFFF": "ac",
		"x\u00ff":                "x\u0100",
		string(rune(0xFFFF)):     string(rune(0x10000)),
		"\u07ff":                 "\u0800",
	} {
		got, ok := pastPrefix(prefix)
		if !ok || got != want {
			t.Errorf("pastPrefix(%q) = %q, %v; want %q", prefix, got, ok, want)
		}
	}
	for _, none := range []string{"", "\U0010FFFF", "\U0010FFFF\U0010FFFF"} {
		if _, ok := pastPrefix(none); ok {
			t.Errorf("pastPrefix(%q) has a bound", none)
		}
	}
}

func TestMaxNameAndAfter(t *testing.T) {
	for _, name := range []string{"a", strings.Repeat("\U0010FFFF", 256), strings.Repeat("\xf4\x8f\xbf\xbf", 255) + "zzzz"} {
		if !(name < maxName) {
			t.Errorf("%q is not below maxName", name)
		}
	}
	if !utf8.ValidString(maxName) || !utf8.ValidString(after("a")) || !(after("a") > "a") || !(after("a") < "a0") {
		t.Error("maxName or after")
	}
}

// validateName never panics, and what it accepts a row and the list's walk can hold: valid
// UTF-8 of 1 to 1024 bytes without NUL, whose successors stay valid UTF-8.
func FuzzValidateName(f *testing.F) {
	for _, s := range []string{"a", "photos/a b.png", "a//b", ".", "..", ".well-known/acme-challenge/x", "\xff", "a\x00", "naïve 'q' 1+1%;x.txt"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		if validateName(name) != nil {
			return
		}
		if len(name) == 0 || len(name) > maxNameBytes || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\r\n") {
			t.Fatalf("validateName accepted %q", name)
		}
		if !(name < maxName) || !utf8.ValidString(after(name)) {
			t.Fatalf("%q breaks the walk's bounds", name)
		}
		if up, ok := pastPrefix(name); ok {
			if !utf8.ValidString(up) || !(up > name) || !(up > name+"\U0010FFFF") {
				t.Fatalf("pastPrefix(%q) = %q", name, up)
			}
		}
		if got := strings.Join(segments(name), "/"); got != name {
			t.Fatalf("segments(%q) join to %q", name, got)
		}
		_ = defaultDisposition(name)
		_ = hasEmptySegment(name)
	})
}

// For any prefix and any name that starts with it, the name is below pastPrefix; and a name that
// does not start with it is either below the prefix or not below pastPrefix.
func FuzzPastPrefix(f *testing.F) {
	f.Add("a/", "a/b")
	f.Add("\u00e9", "\u00e9a")
	f.Add("a\U0010FFFF", "a\U0010FFFFz")
	f.Add("a", "b")
	f.Fuzz(func(t *testing.T, prefix, name string) {
		if !utf8.ValidString(prefix) || !utf8.ValidString(name) {
			return
		}
		up, ok := pastPrefix(prefix)
		if !ok {
			return
		}
		inside := strings.HasPrefix(name, prefix)
		between := name >= prefix && name < up
		if inside != between {
			t.Fatalf("prefix %q, bound %q, name %q: inside %v, between %v", prefix, up, name, inside, between)
		}
	})
}
