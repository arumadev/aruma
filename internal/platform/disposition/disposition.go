// Package disposition is Cloud Storage's one escaping of a file name in Content-Disposition
// (ADR-022 §10.4): the filename* parameter, UTF-8 with no language, of an object name's last
// segment. core stores it in the default disposition of a /v0 upload (inline; …), and media
// sends it in the attachment of an object stored without one; core and media may not import
// each other, so both import this. It imports Go's standard library alone.
//
// The escaping keeps RFC 8187's attr-char (letters, digits and !#$&+-.^_`|~) and percent-encodes
// every other byte in upper-case hex. The one case production shows (PY-RR storedObjects) is
// matched exactly: naïve 'q' 1+1%;x.txt stores as na%C3%AFve%20%27q%27%201+1%25%3Bx.txt, with +
// kept and ' encoded. Provisional beyond it (ADR-023 §3-28).
package disposition

import "strings"

// Prefix starts the filename* parameter: the charset and an empty language.
const Prefix = "filename*=utf-8''"

// LastSegment returns what follows name's last slash: the name a download is saved under. It is
// "" for a name that ends with a slash.
func LastSegment(name string) string {
	return name[strings.LastIndexByte(name, '/')+1:]
}

// Escape percent-encodes every byte of s that is not an attr-char (RFC 8187 §3.2.1).
func Escape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := range len(s) {
		c := s[i]
		if attrChar(c) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xf])
	}
	return b.String()
}

// Filename returns the filename* parameter of name's last segment, "" when it has none (a name
// that ends with a slash).
func Filename(name string) string {
	last := LastSegment(name)
	if last == "" {
		return ""
	}
	return Prefix + Escape(last)
}

// attrChar reports RFC 8187's attr-char.
func attrChar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}
