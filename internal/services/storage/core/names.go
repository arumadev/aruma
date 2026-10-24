package core

import (
	"strings"
	"unicode/utf8"
)

// Object names beyond validateName (helpers.go): what a list leaves out of /v0, and the byte
// order the list walks.

// hasEmptySegment reports whether a name has an empty path segment (a//b, /x, x/): /v0 lists
// leave such names and prefixes out, GCS lists keep them (ADR-022 §13; CT "should not include
// show invalid prefixes and items").
func hasEmptySegment(name string) bool {
	return name == "" || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.Contains(name, "//")
}

// prefixHasEmptySegment is hasEmptySegment for a list's prefix, which ends with the delimiter:
// listAll/bar/ is a folder, listAll// is not (CT "should not include show invalid prefixes and
// items": prefixes bar and baz from listAll/bar// and listAll/baz//qux, none from listAll//foo).
func prefixHasEmptySegment(prefix string) bool {
	return hasEmptySegment(strings.TrimSuffix(prefix, "/"))
}

// maxName is above every object name in byte order: a name is at most 1024 bytes of valid
// UTF-8, and no valid UTF-8 byte is above 0xF4, so 257 repeats of the largest code point outrank
// any of them. It bounds a list that has no upper end.
var maxName = strings.Repeat(string(utf8.MaxRune), maxNameBytes/utf8.UTFMax+1)

// after returns the smallest name above name: name followed by U+0001, as no name holds NUL.
func after(name string) string { return name + "\x01" }

// pastPrefix returns the smallest valid UTF-8 string above every string that starts with
// prefix, so a list skips a prefix's names with one seek (ADR-022 §13). It increments prefix's
// last code point, skipping the surrogates, and drops a last U+10FFFF to increment the one
// before; ok is false when every code point is U+10FFFF and nothing is above.
func pastPrefix(prefix string) (string, bool) {
	for prefix != "" {
		r, size := utf8.DecodeLastRuneInString(prefix)
		prefix = prefix[:len(prefix)-size]
		switch {
		case r == utf8.MaxRune:
			continue
		case r == 0xD800-1:
			r = 0xE000
		default:
			r++
		}
		return prefix + string(r), true
	}
	return "", false
}

// checkListText refuses a prefix, delimiter or offset Postgres cannot compare: invalid UTF-8,
// or NUL, which no name holds.
func checkListText(field, v string) error {
	if !utf8.ValidString(v) || strings.IndexByte(v, 0) >= 0 {
		return invalid("The " + field + " is not valid UTF-8 without NUL.")
	}
	return nil
}
