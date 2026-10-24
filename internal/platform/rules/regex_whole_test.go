package rules

import (
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The whole form of matches(), (?:)^(?:p)$, answers as ^(?:p)$ does on every pattern and
// string: the empty group before it matches the empty string only, wherever it is. Patterns
// with their own anchors, empty matches, flags and alternations whose first branch is shorter
// included.
func TestWholeFormMatchesAsAnchored(t *testing.T) {
	patterns := []string{
		``, `a`, `a*`, `a*?`, `a|ab`, `ab|a`, `(a|)`, `(|a)+`, `a{0}`, `a{2}`, `(?U)a+`,
		`^`, `$`, `^a`, `a$`, `^a$`, `\Aa\z`, `\A`, `\z`, `(?m)^a$`, `(?m)a$\n?`, `(?m)^$`,
		`(?s).*`, `.*`, `.+`, `\b`, `\bfoo\b`, `\B`, `(?i)straße`, `\pN+`, `[\p{L} ]{1,3}`,
		`^[\p{L} ]{1,3}$`, `x*|y`, `(?:)`, `(?:)^`, `a(?:)b`,
	}
	inputs := []string{"", "a", "aa", "ab", "abab", "aaa", "\n", "a\n", "\na", "foo", "x foo y",
		"ab c", "Ahmad", "STRASSE", "straße", "123", "y", "é ü", "a b"}
	lim := DefaultLimits()
	for _, p := range patterns {
		anchored := regexp.MustCompile(`^(?:` + p + `)$`)
		for _, s := range inputs {
			m := newMeter(&lim, NewInflight(lim.InflightBytes), newRegexCache(&lim))
			got, err := matchString(m, s, p)
			if err != nil {
				t.Fatalf("%q.matches(%q): %v", s, p, err)
			}
			if b, _ := got.AsBool(); b != anchored.MatchString(s) {
				t.Errorf("%q.matches(%q) = %v, ^(?:p)$ says %v", s, p, b, !b)
			}
		}
	}
}

// Go builds no one-pass program for the whole form (wholeForm), which would copy the table of
// each class instruction and merge those of an alternation's branches; ^(?:p)$ alone is one.
func TestWholeFormIsNotOnePass(t *testing.T) {
	field, ok := reflect.TypeFor[regexp.Regexp]().FieldByName("onepass")
	if !ok || field.Type.Kind() != reflect.Pointer {
		t.Skip("this Go's regexp keeps no one-pass program where the test looks")
	}
	onepass := func(re *regexp.Regexp) bool { return !reflect.ValueOf(re).Elem().FieldByIndex(field.Index).IsNil() }
	for _, p := range []string{`^[\p{L} ]{1,100}$`, `[\p{L} ]{1,100}`, `abc`, `\Aab`, categories(28)} {
		if !onepass(regexp.MustCompile(`^(?:` + p + `)$`)) {
			t.Errorf("^(?:%s)$ is not one-pass: the test proves nothing", p)
		}
		forms, err := compileForms(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, re := range []*regexp.Regexp{forms.whole, forms.first, forms.next} {
			if onepass(re) {
				t.Errorf("%s is one-pass", re)
			}
		}
	}
}

// Validation patterns of the kind rules hold for names, usernames, addresses and bios, in
// several scripts, with Unicode classes and bounded repetitions (the re-review of S7-B1,
// unicode-repeat-refused: ^[\p{L} ]{1,100}$ fitted three times in a file, and
// ^[\p{L}\p{M}\p{N}\p{P}\p{Zs}]{1,500}$ not once). A file with each of them, twice, compiles,
// and they answer as RE2 does.
func TestUnicodeValidationPatternsCompile(t *testing.T) {
	cases := []struct {
		pattern string
		yes     []string
		no      []string
	}{
		{`^[\p{L} ]{1,100}$`, []string{"Ахмед Ali", "José María", "محمد"}, []string{"", "bob1", strings.Repeat("a", 101)}},
		{`^[\p{L}\p{M} '.-]{1,100}$`, []string{"O'Brien-Smith", "Nguyễn Văn A", "हिन्दी नाम"}, []string{"R2-D2", "a\tb"}},
		{`^[\p{L}\p{N}_.]{3,30}$`, []string{"josé_99", "иван.петров", "用户123"}, []string{"ab", "bad name", "x@y"}},
		{`^[\p{L}\p{M}\p{N}\p{P}\p{Zs}]{1,500}$`, []string{"12 Rue de la Paix, Paris", "東京都千代田区1-1", "شارع الملك فهد، الرياض"}, []string{"", "a\nb", "€5"}},
		{`^\p{Han}{1,20}$`, []string{"张伟", "李小龙"}, []string{"Zhang", "张 伟"}},
		{`^\p{Arabic}+(?: \p{Arabic}+)*$`, []string{"محمد علي"}, []string{"محمد  علي", "Ali"}},
		{`^[\p{Cyrillic} ]{2,50}$`, []string{"Иван Петров"}, []string{"Ivan"}},
		{`^[\p{Hiragana}\p{Katakana}ー]{1,40}$`, []string{"たなか", "タナカ", "ラーメン"}, []string{"田中"}},
		{`^[\p{L}\p{N}._%+-]+@[\p{L}\p{N}.-]+\.\p{L}{2,}$`, []string{"josé@exemplo.com", "пример@почта.рф"}, []string{"a@b", "@x.io"}},
		{`^[\p{L}\p{N}\p{P}\p{S}\p{Zs}]{0,280}$`, []string{"", "Hello, мир! 👋"}, []string{"tab\there"}},
	}
	var lines []string
	for i, c := range cases {
		for k := range 2 {
			lines = append(lines, fmt.Sprintf("      allow read: if \"x%d\".matches(\"%s\");", 2*i+k, strings.ReplaceAll(c.pattern, `\`, `\\`)))
		}
	}
	lim := DefaultLimits()
	rs, issues := Compile(Storage, File{Name: "storage.rules", Content: storageFile(lines)}, lim)
	if rs == nil {
		t.Fatalf("the file does not compile: %v", issues.Err())
	}
	t.Logf("%d patterns: they take %d of regex_bytes %d; the ruleset weighs %d", len(cases), rs.regexes.literalBytes(), lim.RegexBytes, rs.Weight())
	if rs.regexes.literalBytes() > lim.RegexBytes/4 {
		t.Errorf("the patterns take %d, more than a quarter of regex_bytes", rs.regexes.literalBytes())
	}
	for _, c := range cases {
		for _, s := range append(append([]string{}, c.yes...), c.no...) {
			want := regexp.MustCompile(c.pattern).MatchString(s)
			m := newMeter(&lim, NewInflight(lim.InflightBytes), rs.regexes)
			got, err := matchString(m, s, c.pattern)
			if b, _ := got.AsBool(); err != nil || b != want || want != slices.Contains(c.yes, s) {
				t.Errorf("%q.matches(%q) = %v, %v; want %v", s, c.pattern, b, err, want)
			}
		}
	}
}
