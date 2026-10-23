package rules

// pathPart is one part of a path literal, as the interpreter has evaluated it.
type pathPart struct {
	text  string // literal text as written, still URL-encoded
	value Value  // the value of a $(…)
	isVal bool   // the part is a $(…)
}

// buildPath builds the Path value of a path literal: literal text is URL-decoded as Java's
// URLDecoder does, a $(x) string is one segment, and a $(p) path inserts all of its segments.
// S1-B replaces this stub.
func buildPath(m *meter, parts []pathPart) (Value, error) {
	panic("S1-B: buildPath")
}
