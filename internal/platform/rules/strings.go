package rules

import (
	"encoding/base64"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// The units of strings (wtf8.go): sizes, indexes and slices count UTF-16 units, and every
// walk over a string is charged to the meter's work by its length.

// stringUnits charges a walk over s and returns its length in UTF-16 units.
func stringUnits(m *meter, s string) (int, error) {
	if err := m.work(walkWork(len(s))); err != nil {
		return 0, err
	}
	return utf16Len(s), nil
}

// stringIndex is s[i]: the walk to unit i is charged before it runs, a unit being three bytes
// at most, and the whole string's only when i is past its end, for the message's size.
func stringIndex(m *meter, s string, i int64) (Value, error) {
	if i >= 0 && i < int64(len(s)) {
		if err := m.work(walkWork(min(len(s), 3*int(i)+4))); err != nil {
			return Value{}, err
		}
		a, b, ok := unitOffsets(s, int(i), int(i)+1)
		if ok {
			return sliceAtCharged(m, s, a, b)
		}
	}
	size, err := stringUnits(m, s)
	if err != nil {
		return Value{}, err
	}
	return Value{}, errIndex(i, size)
}

// sliceString returns units from to to of s, which the caller has checked, charging the walk to
// them and the bytes of the result before it builds it.
func sliceString(m *meter, s string, from, to int) (Value, error) {
	if err := m.work(walkWork(len(s))); err != nil {
		return Value{}, err
	}
	a, b, _ := unitOffsets(s, from, to)
	return sliceAtCharged(m, s, a, b)
}

// sliceAtCharged is sliceAt, its result charged before it is built.
func sliceAtCharged(m *meter, s string, a, b unitPos) (Value, error) {
	size := b.b - a.b
	if a.mid {
		size -= 1 // the four bytes of the pair become the three of its low half
	}
	if b.mid {
		size += 3
	}
	if err := chargeString(m, size); err != nil {
		return Value{}, err
	}
	return String(sliceAt(s, a, b)), nil
}

// compareUTF16 orders two strings by UTF-16 unit, as Java's compareTo does. Past their common
// bytes, which are whole characters of both, it compares unit by unit: that differs from byte
// order only between a character past U+FFFF, whose first unit is a surrogate, and one from
// U+E000 to U+FFFF, and where a lone surrogate meets a pair. The caller charges
// walkWork(min(len(a), len(b))).
func compareUTF16(a, b string) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	for i > 0 && i < len(a) && a[i]&0xC0 == 0x80 {
		i-- // back to the start of the character that differs
	}
	x, y := unitIter{s: a, i: i}, unitIter{s: b, i: i}
	for {
		u, okA := x.next()
		v, okB := y.next()
		switch {
		case !okA && !okB:
			return 0
		case !okA:
			return -1
		case !okB:
			return 1
		case u != v:
			return compareInts(int64(u), int64(v))
		}
	}
}

// stringMethod calls s.name(args).
func stringMethod(m *meter, s string, name string, args []Value) (Value, error) {
	noArgs := func() error {
		if len(args) != 0 {
			return mismatch("string."+name, args, []string{})
		}
		return nil
	}
	switch name {
	case "size":
		if err := noArgs(); err != nil {
			return Value{}, err
		}
		n, err := stringUnits(m, s)
		if err != nil {
			return Value{}, err
		}
		return Int(int64(n)), nil
	case "lower", "upper":
		// Only ASCII letters change case (hidane, the emulator; production records only ASCII).
		if err := noArgs(); err != nil {
			return Value{}, err
		}
		if err := chargeString(m, len(s)); err != nil {
			return Value{}, err
		}
		if name == "lower" {
			return String(asciiCase(s, 'A', 'Z', 'a'-'A')), nil
		}
		return String(asciiCase(s, 'a', 'z', 'A'-'a')), nil
	case "trim":
		// Java's trim: control characters and spaces, U+0000 to U+0020.
		if err := noArgs(); err != nil {
			return Value{}, err
		}
		t := strings.TrimFunc(s, func(r rune) bool { return r <= ' ' })
		return newString(m, t)
	case "toUtf8":
		if err := noArgs(); err != nil {
			return Value{}, err
		}
		// A lone surrogate is '?', as Java's getBytes(UTF_8) writes it.
		if err := m.work(scanWork(len(s))); err != nil {
			return Value{}, err
		}
		if err := chargeString(m, len(s)); err != nil {
			return Value{}, err
		}
		return Value{kind: KindBytes, x: toUTF8(s)}, nil
	case "matches", "split":
		if len(args) != 1 || args[0].kind != KindString {
			return Value{}, mismatch("string."+name, args, []string{"string"})
		}
		if name == "matches" {
			return matchString(m, s, args[0].str())
		}
		return splitString(m, s, args[0].str())
	case "replace":
		if len(args) != 2 || args[0].kind != KindString || args[1].kind != KindString {
			return Value{}, mismatch("string.replace", args, []string{"string", "string"})
		}
		return replaceString(m, s, args[0].str(), args[1].str())
	}
	return Value{}, errNotFound(name)
}

// asciiCase shifts the letters from lo to hi by delta.
func asciiCase(s string, lo, hi byte, delta int) string {
	b := []byte(s)
	for i, c := range b {
		if c >= lo && c <= hi {
			b[i] = byte(int(c) + delta)
		}
	}
	return string(b)
}

// bytesMethod calls b.name(args).
func bytesMethod(m *meter, b string, name string, args []Value) (Value, error) {
	if len(args) != 0 {
		switch name {
		case "size", "toBase64", "toHexString":
			return Value{}, mismatch("bytes."+name, args, []string{})
		}
	}
	switch name {
	case "size":
		return Int(int64(len(b))), nil
	case "toBase64":
		// URL-safe and padded: b'\xff'.toBase64() == '_w==' (Pyric bytes-literal, production).
		n := base64.URLEncoding.EncodedLen(len(b))
		if err := chargeString(m, n); err != nil {
			return Value{}, err
		}
		if err := m.alloc(int64(len(b))); err != nil { // the bytes, copied for the encoder
			return Value{}, err
		}
		return String(base64.URLEncoding.EncodeToString([]byte(b))), nil
	case "toHexString":
		// Upper case (Pyric bytes-toutf8-and-hashing: "md5 uppercase production representation").
		if err := chargeString(m, 2*len(b)); err != nil {
			return Value{}, err
		}
		const digits = "0123456789ABCDEF"
		var out strings.Builder
		out.Grow(2 * len(b))
		for i := 0; i < len(b); i++ {
			out.WriteByte(digits[b[i]>>4])
			out.WriteByte(digits[b[i]&0x0F])
		}
		return String(out.String()), nil
	}
	return Value{}, errNotFound(name)
}

// toString is string(v): ints, floats as Java prints them, null, strings, bools and paths.
// Anything else is an error (Pyric conversion-functions, time-math-and-casts).
func toString(v Value) (string, error) {
	switch v.kind {
	case KindInt:
		return strconv.FormatInt(v.integer(), 10), nil
	case KindFloat:
		return javaDouble(v.float()), nil
	case KindNull:
		return "null", nil
	case KindString:
		return v.str(), nil
	case KindBool:
		return strconv.FormatBool(v.boolean()), nil
	case KindPath:
		return pathString(v.segments()), nil
	}
	if err := reserved(v); err != nil {
		return "", err
	}
	return "", errUnsupported("string("+v.kind.String()+")",
		signatures("string", []string{"int"}, []string{"float"}, []string{"null"}, []string{"string"},
			[]string{"bool"}, []string{"path"}))
}

// javaDouble writes f as Java's Double.toString does (JDK 19 and later, which production
// matches): at least two significant digits, the shortest that read back as f otherwise, plain
// from 10^-3 up to 10^7 and in E notation outside: 1.0, 0.001, 1.0E7, 1.0E-4, 9.9E-324
// (Pyric time-math-and-casts and conversion-functions, production).
func javaDouble(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	// The shortest digits, and of two digits the nearest when one would do: Java writes at
	// least two, and the nearest of two digits reads back as f whenever one digit does.
	e := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	if len(digits) == 1 {
		mant, exp, _ = strings.Cut(strconv.FormatFloat(f, 'e', 1, 64), "e")
		digits = strings.Replace(mant, ".", "", 1)
	}
	digits = strings.TrimRight(digits, "0")
	if digits == "" {
		digits = "0"
	}
	x, _ := strconv.Atoi(exp)
	if f >= 1e-3 && f < 1e7 {
		point := x + 1
		var whole, frac string
		if point <= 0 {
			whole, frac = "0", strings.Repeat("0", -point)+digits
		} else {
			if len(digits) < point {
				digits += strings.Repeat("0", point-len(digits))
			}
			whole, frac = digits[:point], digits[point:]
		}
		if frac == "" {
			frac = "0"
		}
		return sign + whole + "." + frac
	}
	rest := digits[1:]
	if rest == "" {
		rest = "0"
	}
	return sign + digits[:1] + "." + rest + "E" + strconv.Itoa(x)
}

// parseJavaDouble reads s as Java's Double.parseDouble does: control characters and spaces
// around it are ignored; NaN and Infinity, with a sign; decimal digits with a point and an
// exponent; hexadecimal with a binary exponent (0x1p3); an f, F, d or D after a number.
// Production takes all of these (Pyric conversion-functions: float('0x1p3') == 8.0,
// float('1.5f'), float(' 2.5')) and refuses inf, nan, 1_000 and 1.2.3; hidane does not read
// hexadecimal, and production wins.
func parseJavaDouble(s string) (float64, bool) {
	t := strings.TrimFunc(s, func(r rune) bool { return r <= ' ' })
	body := t
	sign := 1.0
	if body != "" && (body[0] == '+' || body[0] == '-') {
		if body[0] == '-' {
			sign = -1
		}
		body = body[1:]
	}
	switch body {
	case "NaN":
		return math.NaN(), true
	case "Infinity":
		return math.Inf(int(sign)), true
	}
	if n := len(body); n > 0 && strings.IndexByte("fFdD", body[n-1]) >= 0 {
		body = body[:n-1]
	}
	if !javaNumber(body) {
		return 0, false
	}
	f, err := strconv.ParseFloat(body, 64)
	if err != nil {
		var ne *strconv.NumError
		if !errors.As(err, &ne) || ne.Err != strconv.ErrRange {
			return 0, false
		}
	}
	return sign * f, true
}

// javaNumber reports whether s is an unsigned floating-point literal of Java's grammar, without
// its suffix.
func javaNumber(s string) bool {
	digits := func(s string, hex bool) (int, string) {
		n := 0
		for n < len(s) && (s[n] >= '0' && s[n] <= '9' ||
			hex && (s[n] >= 'a' && s[n] <= 'f' || s[n] >= 'A' && s[n] <= 'F')) {
			n++
		}
		return n, s[n:]
	}
	exponent := func(s string, mark string) bool {
		if s == "" || !strings.ContainsRune(mark, rune(s[0])) {
			return false
		}
		s = s[1:]
		if s != "" && (s[0] == '+' || s[0] == '-') {
			s = s[1:]
		}
		n, rest := digits(s, false)
		return n > 0 && rest == ""
	}
	hex := len(s) > 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X')
	if hex {
		s = s[2:]
	}
	n, rest := digits(s, hex)
	if rest != "" && rest[0] == '.' {
		var m int
		m, rest = digits(rest[1:], hex)
		n += m
	}
	if n == 0 {
		return false
	}
	if hex {
		return exponent(rest, "pP")
	}
	return rest == "" || exponent(rest, "eE")
}

// parseJavaLong reads s as Java's Long.parseLong does: a sign and decimal digits, nothing else.
// A digit is any Unicode decimal digit, as Character.digit reads it (production, the review's
// probes of the Rules Test API, 2026-10-24: int('١٢') == 12, int('１') == 1).
func parseJavaLong(s string) (int64, bool) {
	body := s
	neg := false
	if body != "" && (body[0] == '+' || body[0] == '-') {
		neg = body[0] == '-'
		body = body[1:]
	}
	if body == "" {
		return 0, false
	}
	// Accumulated negatively, as Java does, so the smallest int64 fits.
	var n int64
	for _, r := range body {
		d, ok := digitValue(r)
		if !ok || n < (math.MinInt64+int64(d))/10 {
			return 0, false
		}
		n = n*10 - int64(d)
	}
	if !neg {
		if n == math.MinInt64 {
			return 0, false
		}
		n = -n
	}
	return n, true
}

// digitValue is Character.digit(r, 10): the value of a Unicode decimal digit (category Nd).
// Unicode encodes those in runs of ten from zero, so r's value is its place in its run.
func digitValue(r rune) (int, bool) {
	if r >= '0' && r <= '9' {
		return int(r - '0'), true
	}
	if r < 0x80 || !unicode.IsDigit(r) {
		return 0, false
	}
	k := 0
	for unicode.IsDigit(r-rune(k)-1) && k < 100 {
		k++
	}
	return k % 10, true
}
