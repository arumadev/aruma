package wire

import "errors"

// Range is the one byte range a media request asks for.
type Range struct {
	Start  int64
	Length int64
}

// ErrUnsatisfiable is a range that starts after a non-empty object's last byte: 416 with
// Content-Range: bytes */<size> (ADR-022 §10.3).
var ErrUnsatisfiable = errors.New("wire: range not satisfiable")

// ParseRange parses a Range header for an object of size bytes. It returns false when the whole
// object is to be sent with 200: no header, an empty object (whatever the range, E §4), several
// ranges, or a form it ignores. A single bytes=a-b, bytes=a- or bytes=-n is a 206.
func ParseRange(header string, size int64) (Range, bool, error) { panic("S2-B: ParseRange") }
