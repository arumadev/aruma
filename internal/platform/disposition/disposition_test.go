package disposition

import (
	"strings"
	"testing"
)

// PY-RR storedObjects, the one case production shows, and the cases around it: + kept, ' as
// %27, every other byte outside attr-char percent-encoded in upper-case hex.
func TestFilename(t *testing.T) {
	cases := []struct{ name, want string }{
		{"naïve 'q' 1+1%;x.txt", "filename*=utf-8''na%C3%AFve%20%27q%27%201+1%25%3Bx.txt"}, // PY-RR
		{"dir/naïve 'q' 1+1%;x.txt", "filename*=utf-8''na%C3%AFve%20%27q%27%201+1%25%3Bx.txt"},
		{"a/b/plain.txt", "filename*=utf-8''plain.txt"},
		{"!#$&+-.^_`|~", "filename*=utf-8''!#$&+-.^_`|~"},
		{`"quoted";,=\*`, "filename*=utf-8''%22quoted%22%3B%2C%3D%5C%2A"},
		{"تقرير.pdf", "filename*=utf-8''%D8%AA%D9%82%D8%B1%D9%8A%D8%B1.pdf"},
		{"line\r\nbreak\x00\x7f", "filename*=utf-8''line%0D%0Abreak%00%7F"},
		{"dir/", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := Filename(c.name); got != c.want {
			t.Errorf("Filename(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

// No byte escapes to anything that could end the parameter or the header.
func TestEscapeEveryByte(t *testing.T) {
	for c := range 256 {
		esc := Escape(string([]byte{byte(c)}))
		if strings.ContainsAny(esc, "\r\n\"; ,'\x00\x7f\\") {
			t.Errorf("byte %#x escapes to %q", c, esc)
		}
		if (len(esc) != 1) == attrChar(byte(c)) {
			t.Errorf("byte %#x escapes to %q", c, esc)
		}
	}
}

func FuzzEscape(f *testing.F) {
	f.Add("naïve 'q' 1+1%;x.txt")
	f.Fuzz(func(t *testing.T, s string) {
		esc := Escape(s)
		for i := range len(esc) {
			if c := esc[i]; c != '%' && !attrChar(c) {
				t.Fatalf("Escape(%q) = %q holds %q", s, esc, c)
			}
		}
	})
}
