package wire

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

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
//
// RFC 9110 §14.2 lets a server ignore a Range it does not understand, so every malformed header
// (another unit, a-b with b < a, a sign, a missing number) is a 200 too. Only a well-formed range
// that starts at or after the last byte, or a suffix of zero bytes, is ErrUnsatisfiable. Numbers
// too large for an int64 count as larger than any object. Several ranges are found by their
// comma alone, so a header of a billion of them costs one scan and nothing more.
func ParseRange(header string, size int64) (Range, bool, error) {
	if header == "" || size <= 0 {
		return Range{}, false, nil
	}
	unit, spec, ok := strings.Cut(header, "=")
	if !ok || !strings.EqualFold(strings.Trim(unit, " \t"), "bytes") {
		return Range{}, false, nil
	}
	if strings.IndexByte(spec, ',') >= 0 {
		return Range{}, false, nil
	}
	spec = strings.Trim(spec, " \t")
	first, last, ok := strings.Cut(spec, "-")
	if !ok {
		return Range{}, false, nil
	}
	if first == "" { // bytes=-n: the last n bytes
		n, ok := digits(last)
		if !ok {
			return Range{}, false, nil
		}
		if n == 0 {
			return Range{}, false, ErrUnsatisfiable
		}
		n = min(n, size)
		return Range{Start: size - n, Length: n}, true, nil
	}
	start, ok := digits(first)
	if !ok {
		return Range{}, false, nil
	}
	end := int64(math.MaxInt64)
	if last != "" {
		if end, ok = digits(last); !ok || end < start {
			return Range{}, false, nil
		}
	}
	if start >= size {
		return Range{}, false, ErrUnsatisfiable
	}
	end = min(end, size-1)
	return Range{Start: start, Length: end - start + 1}, true, nil
}

// digits parses a non-empty run of ASCII digits, saturating at math.MaxInt64.
func digits(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	var n int64
	for i := range len(s) {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		if n > (math.MaxInt64-int64(c-'0'))/10 {
			n = math.MaxInt64
			continue
		}
		n = n*10 + int64(c-'0')
	}
	return n, true
}

// ContentRange returns the Content-Range of a 206 serving r from an object of size bytes:
// bytes <first>-<last>/<size> (Pyric's rangeGet on production: "bytes 2-5/64").
func (r Range) ContentRange(size int64) string {
	return "bytes " + strconv.FormatInt(r.Start, 10) + "-" + strconv.FormatInt(r.Start+r.Length-1, 10) +
		"/" + strconv.FormatInt(size, 10)
}

// UnsatisfiedContentRange returns the Content-Range of a 416 for an object of size bytes:
// bytes */<size> (ADR-022 §10.3).
func UnsatisfiedContentRange(size int64) string {
	return "bytes */" + strconv.FormatInt(size, 10)
}
