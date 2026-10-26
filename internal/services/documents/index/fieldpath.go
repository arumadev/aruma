package index

import (
	"slices"
	"strings"
)

// NameField is the document key's field path. Every index ends with it.
const NameField = "__name__"

// pathSegments splits a canonical field path into its unescaped segments, and returns where
// each segment ends in path, so a prefix of segments is path[:end] in canonical form. A
// backtick-quoted segment may hold dots, and \` and \\ escapes. This package does not validate
// paths (the query package does); a malformed path splits as best it can.
func pathSegments(path string) (segs []string, ends []int) {
	for i := 0; i < len(path); {
		var seg strings.Builder
		if path[i] == '`' {
			i++
			for i < len(path) && path[i] != '`' {
				if path[i] == '\\' && i+1 < len(path) {
					i++
				}
				seg.WriteByte(path[i])
				i++
			}
			i++ // the closing backtick
		} else {
			plain, _, _ := strings.Cut(path[i:], ".")
			seg.WriteString(plain)
			i += len(plain)
		}
		segs = append(segs, seg.String())
		ends = append(ends, min(i, len(path)))
		i++ // the dot
	}
	return segs, ends
}

// comparePaths orders field paths segment by segment, as Firestore's FieldPath does: the order
// in which implicit inequality orders are appended (query.proto, StructuredQuery.order_by) and
// in which production lists them in a suggested index (R16).
func comparePaths(a, b string) int {
	sa, _ := pathSegments(a)
	sb, _ := pathSegments(b)
	return slices.Compare(sa, sb)
}

// ancestors are the canonical paths of path's proper prefixes, nearest first: "m.x.y" has
// "m.x" and "m". A field override on an ancestor applies to its subfields (overrides.go).
func ancestors(path string) []string {
	_, ends := pathSegments(path)
	var out []string
	for i := len(ends) - 2; i >= 0; i-- {
		out = append(out, path[:ends[i]])
	}
	return out
}
