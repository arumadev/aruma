package value

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"math/big"
	"math/bits"
	"strconv"
	"strings"
)

// Key returns v's sort key: bytes.Compare(Key(a), Key(b)) == Compare(a, b) for any values, so
// an index stored as Postgres bytea orders as production does. Equal values (1, 1.0, int32 1
// and the decimal 1.0; -0.0 and 0) have the same key, and so do values the 1500-byte index
// budget cuts to the same prefix.
//
// The encoding, built to be read in order:
//
//	key         KeyVersion, then value
//	value       tag byte, then by type:
//	  0x10 null
//	  0x18 MinKey
//	  0x20 boolean       0x00 | 0x01
//	  0x30 number        number
//	  0x40 timestamp     seconds and nanos, big-endian with the sign bit flipped (8 and 4 bytes)
//	  0x48 BSON timestamp seconds and increment, big-endian (4 and 4 bytes)
//	  0x50 string        text
//	  0x60 bytes         text (the bytes)
//	  0x68 BSON binary   text (the subtype byte, then the data)
//	  0x70 reference     text(project) text(database), then per segment 0x01 and the numeric
//	                     id of __id<n>__ as 8 sign-flipped bytes, or 0x02 and text(segment); 0x00
//	  0x78 ObjectId      its 12 bytes
//	  0x80 geopoint      number(latitude) number(longitude)
//	  0x88 regex         text(pattern) text(options)
//	  0x90 array         the elements' keys, then 0x00
//	  0xA0 vector        the length as 8 bytes, then per element 0x01 and its IEEE bits in total
//	                     order (sign bit set for positives, all bits flipped for negatives)
//	  0xB0 map           per field in name order 0x01 text(name) and the value's key; then 0x00
//	  0xC0 MaxKey
//	text        the bytes with 0x00 written 0x00 0xFF and 0xFF written 0xFF 0x00, then 0x00 0x01
//	number      a class byte: 0x01 NaN, 0x02 -Inf, 0x03 negative, 0x04 zero, 0x05 positive,
//	            0x06 +Inf. A nonzero finite number m = 1.f × 2^e then has e+0x8000 as 2 bytes and
//	            the first 64 bits of f, then 0x00 when they hold m exactly, as they do every
//	            integer and double, or else (a Decimal128 such as 0.1) 0x01 and m in decimal:
//	            0.d₁d₂… × 10^E as E+0x8000 in 2 bytes, the digits in pairs as 1+10a+b (the last
//	            padded with 0), and 0x00. All of it is flipped for a negative number. Integers,
//	            doubles, int32s and decimals share it, so 2^53 as any of them has one key.
//	cut         0xFF 0xFF, where the index budget (see Compare) ends inside the value; the
//	            key stops there.
//
// The terminators sort below every byte a sequence can continue with (an element's tag, a
// field's 0x01, an escaped byte) and the cut mark above all of them, which gives "shorter
// first" and "cut after its prefixes".
func Key(v Value) []byte { return AppendKey(nil, v) }

// KeyVersion is the first byte of every key: the layout above. A later layout takes another
// version, so keys of both can live in one index while it is rebuilt, each version sorting
// apart.
const KeyVersion = 0x01

// AppendKey appends v's sort key to dst.
func AppendKey(dst []byte, v Value) []byte {
	e := encoder{buf: append(dst, KeyVersion), r: IndexedLimit}
	e.value(v)
	return e.buf
}

// Truncated reports whether the index keeps only part of v: the 1500-byte budget (see Compare)
// ends inside it, so it ties with every value that agrees on the part kept.
func Truncated(v Value) bool {
	e := encoder{r: IndexedLimit}
	e.value(v)
	return e.cut
}

// IndexedWhole reports whether the index holds all of v: nothing is cut and IndexSize is within
// IndexedLimit. Production refuses an == or in operand that is not ("value for v is too large to
// be used in a query", even [x×1499, []], whose empty array is cut), and a cursor that is neither
// whole nor Truncated ("the cursor value is too large": x×1498+"é" and [x×1492, 1], whose last
// character or number overruns the budget and is kept whole).
func IndexedWhole(v Value) bool { return IndexSize(v) <= IndexedLimit && !Truncated(v) }

const (
	tagNull          = 0x10
	tagMinKey        = 0x18
	tagBoolean       = 0x20
	tagNumber        = 0x30
	tagTimestamp     = 0x40
	tagBSONTimestamp = 0x48
	tagString        = 0x50
	tagBytes         = 0x60
	tagBinary        = 0x68
	tagReference     = 0x70
	tagObjectID      = 0x78
	tagGeoPoint      = 0x80
	tagRegex         = 0x88
	tagArray         = 0x90
	tagVector        = 0xA0
	tagMap           = 0xB0
	tagMaxKey        = 0xC0

	end     = 0x00 // after an array's, map's or reference's last item
	item    = 0x01 // before a map field, a vector element or a numeric path segment
	segText = 0x02 // before any other path segment

	exact   = 0x00 // a number's magnitude bits hold it exactly
	inexact = 0x01 // they do not: its decimal digits follow

	exponentBias = 0x8000
)

// tags gives each rank its tag.
var tags = [...]byte{rankNull: tagNull, rankMinKey: tagMinKey, rankBoolean: tagBoolean, rankNumber: tagNumber,
	rankTimestamp: tagTimestamp, rankBSONTimestamp: tagBSONTimestamp, rankString: tagString, rankBytes: tagBytes,
	rankBinary: tagBinary, rankReference: tagReference, rankObjectID: tagObjectID, rankGeoPoint: tagGeoPoint,
	rankRegex: tagRegex, rankArray: tagArray, rankVector: tagVector, rankMap: tagMap, rankMaxKey: tagMaxKey}

// encoder writes a key while spending the index budget exactly as comparer does.
type encoder struct {
	buf []byte
	r   int
	cut bool
}

// starts writes the cut mark and reports false when no budget is left for an item.
func (e *encoder) starts() bool { return e.keep(e.r > 0) }

func (e *encoder) keep(ok bool) bool {
	if !ok {
		e.buf = append(e.buf, 0xFF, 0xFF)
		e.cut = true
	}
	return ok
}

func (e *encoder) value(v Value) {
	if !e.starts() {
		return
	}
	t := classify(v)
	e.buf = append(e.buf, tags[t.rank])
	switch t.rank {
	case rankNull, rankMinKey, rankMaxKey:
		e.r--
	case rankBoolean:
		e.buf = append(e.buf, byte(v.i))
		e.r--
	case rankNumber:
		n := numberOf(t)
		e.buf = appendNumber(e.buf, n)
		e.r -= n.size()
	case rankTimestamp:
		e.buf = binary.BigEndian.AppendUint64(e.buf, uint64(v.i)^1<<63)
		e.buf = binary.BigEndian.AppendUint32(e.buf, uint32(v.n)^1<<31)
		e.r -= 8
	case rankBSONTimestamp:
		e.buf = binary.BigEndian.AppendUint64(e.buf, t.b.ts)
		e.r -= 8
	case rankString:
		e.r--
		e.text(v.s)
	case rankBytes:
		e.bytes(v.s)
	case rankBinary:
		e.bytes(t.b.s)
	case rankReference:
		e.r -= 16
		e.reference(v.s)
	case rankObjectID:
		e.buf = append(e.buf, t.b.s...)
		e.r -= 12
	case rankGeoPoint:
		e.buf = appendNumber(e.buf, number{f: v.f})
		e.buf = appendNumber(e.buf, number{f: v.g})
		e.r -= 16
	case rankRegex:
		e.r--
		if e.text(t.b.s); e.cut || !e.starts() {
			return
		}
		e.r--
		e.text(t.b.o)
	case rankArray:
		for _, x := range v.list {
			if e.value(x); e.cut {
				return
			}
		}
		e.buf = append(e.buf, end)
	case rankVector:
		els := v.vectorElements()
		e.buf = binary.BigEndian.AppendUint64(e.buf, uint64(len(els)))
		for _, x := range els {
			if !e.keep(e.r >= 8) {
				return
			}
			e.r -= 8
			e.buf = append(e.buf, item)
			e.buf = binary.BigEndian.AppendUint64(e.buf, vectorBits(x.f))
		}
	default:
		for _, f := range v.m {
			if !e.starts() {
				return
			}
			e.buf = append(e.buf, item)
			e.r--
			if e.text(f.Name); e.cut {
				return
			}
			if e.value(f.Value); e.cut {
				return
			}
		}
		e.buf = append(e.buf, end)
	}
}

// text writes the characters of s the budget keeps, then its terminator or the cut mark.
func (e *encoder) text(s string) {
	k, cut := keptText(s, e.r)
	e.r -= k
	e.buf = appendEscaped(e.buf, s[:k])
	if e.keep(!cut) {
		e.buf = append(e.buf, 0x00, 0x01)
	}
}

// bytes writes the bytes of s the budget keeps, then its terminator or the cut mark.
func (e *encoder) bytes(s string) {
	k := min(len(s), max(e.r, 0))
	e.r -= k
	e.buf = appendEscaped(e.buf, s[:k])
	if e.keep(k == len(s)) {
		e.buf = append(e.buf, 0x00, 0x01)
	}
}

func (e *encoder) reference(name string) {
	project, database, path := splitName(name)
	e.buf = append(appendEscaped(e.buf, project), 0x00, 0x01)
	e.buf = append(appendEscaped(e.buf, database), 0x00, 0x01)
	for _, seg := range path {
		if !e.starts() {
			return
		}
		e.r--
		if n, ok := numericID(seg); ok {
			e.buf = append(e.buf, item)
			if !e.starts() {
				return
			}
			e.r -= len(seg)
			e.buf = binary.BigEndian.AppendUint64(e.buf, uint64(n)^1<<63)
			continue
		}
		e.buf = append(e.buf, segText)
		if e.text(seg); e.cut {
			return
		}
	}
	e.buf = append(e.buf, end)
}

// appendEscaped appends s with 0x00 and 0xFF escaped, so that neither a terminator nor the cut
// mark can be mistaken for content.
func appendEscaped(dst []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case 0x00:
			dst = append(dst, 0x00, 0xFF)
		case 0xFF:
			dst = append(dst, 0xFF, 0x00)
		default:
			dst = append(dst, c)
		}
	}
	return dst
}

// appendNumber appends a number's class and, when finite and nonzero, its exact magnitude.
func appendNumber(dst []byte, n number) []byte {
	class := n.class()
	dst = append(dst, class)
	if class != classNegative && class != classPositive {
		return dst
	}
	start := len(dst)
	switch {
	case n.kind == BSONDecimal128:
		dst = appendDecimal(dst, n.d)
	case n.isInt:
		u := uint64(n.i)
		if n.i < 0 {
			u = -u
		}
		exp := bits.Len64(u) - 1
		dst = appendMagnitude(dst, exp, u<<(64-exp)) // a shift by 64 gives 0
		dst = append(dst, exact)
	default:
		b := math.Float64bits(n.f)
		biased := int(b>>52) & 0x7FF
		mant := b & (1<<52 - 1)
		if biased == 0 { // subnormal: mant × 2^-1074
			l := bits.Len64(mant)
			dst = appendMagnitude(dst, l-1-1074, mant<<(64-(l-1)))
		} else {
			dst = appendMagnitude(dst, biased-1023, mant<<12)
		}
		dst = append(dst, exact)
	}
	if class == classNegative {
		for i := start; i < len(dst); i++ {
			dst[i] = ^dst[i]
		}
	}
	return dst
}

// appendMagnitude appends 1.frac × 2^exp's exponent and fraction bits.
func appendMagnitude(dst []byte, exp int, frac uint64) []byte {
	dst = binary.BigEndian.AppendUint16(dst, uint16(exp+exponentBias))
	return binary.BigEndian.AppendUint64(dst, frac)
}

var two64 = new(big.Int).Lsh(big.NewInt(1), 64)

// appendDecimal appends a finite nonzero decimal's magnitude: its first 65 significant bits,
// rounded down, then exact, or inexact and its digits.
func appendDecimal(dst []byte, d decimal) []byte {
	num, den := new(big.Int).Set(d.coef), big.NewInt(1)
	if d.exp >= 0 {
		num.Mul(num, pow10(d.exp))
	} else {
		den = pow10(-d.exp)
	}
	// num/den lies in [2^(e-1), 2^(e+1)) for e below, so its leading bit is 2^e or 2^(e-1).
	exp := num.BitLen() - den.BitLen()
	q, r := scaleQuo(num, den, exp)
	if q.BitLen() < 65 {
		exp--
		q, r = scaleQuo(num, den, exp)
	}
	dst = appendMagnitude(dst, exp, new(big.Int).Sub(q, two64).Uint64())
	if r.Sign() == 0 {
		return append(dst, exact)
	}
	digits, e := d.digits()
	dst = binary.BigEndian.AppendUint16(append(dst, inexact), uint16(e+exponentBias))
	for i := 0; i < len(digits); i += 2 {
		pair := 10 * (digits[i] - '0')
		if i+1 < len(digits) {
			pair += digits[i+1] - '0'
		}
		dst = append(dst, 1+pair)
	}
	return append(dst, end)
}

// scaleQuo returns the quotient and remainder of num × 2^(64-exp) / den.
func scaleQuo(num, den *big.Int, exp int) (q, r *big.Int) {
	if exp <= 64 {
		return new(big.Int).QuoRem(new(big.Int).Lsh(num, uint(64-exp)), den, new(big.Int))
	}
	return new(big.Int).QuoRem(num, new(big.Int).Lsh(den, uint(exp-64)), new(big.Int))
}

// ErrCutKey is returned by DecodeKey for the key of a value the index budget cut: it holds a
// prefix only.
var ErrCutKey = errors.New("value: the key is cut short by the index budget")

var errBadKey = errors.New("value: malformed sort key")

// maxKeyDepth bounds the arrays and maps DecodeKey enters, one more than a valid value nests
// (MaxDepth), so that a forged key cannot exhaust the stack.
const maxKeyDepth = MaxDepth + 1

// DecodeKey returns a value with key k: Compare(DecodeKey(Key(v)), v) == 0 for every v the
// budget does not cut and Validate accepts. What a key cannot tell apart comes back in one form:
// a number is an integer when its value is one within int64, else a double when it is one, else
// a Decimal128; -0.0 comes back as 0. An int32 spends 4 bytes of the index budget where an
// integer spends 8, which the key does not record: when integers would spend more than the key
// had left, those within int32 come back as int32s. A key nested deeper than MaxDepth+1 arrays
// and maps, which no valid value is, is refused.
func DecodeKey(k []byte) (Value, error) {
	if len(k) == 0 || k[0] != KeyVersion {
		return Value{}, errBadKey
	}
	var v Value
	for _, int32s := range []bool{false, true} {
		d := decoder{b: k[1:], int32s: int32s}
		var err error
		if v, err = d.value(); err != nil {
			return Value{}, err
		}
		// Bytes no value encodes to (trailing bytes, a reference that splits otherwise) are not
		// a key.
		if len(d.b) == 0 && bytes.Equal(Key(v), k) {
			return v, nil
		}
	}
	return Value{}, errBadKey
}

type decoder struct {
	b      []byte
	depth  int
	int32s bool // integers within int32 as int32s, the numbers that spend least
}

func (d *decoder) byte() (byte, error) {
	if len(d.b) == 0 {
		return 0, errBadKey
	}
	c := d.b[0]
	d.b = d.b[1:]
	return c, nil
}

func (d *decoder) fixed(n int) ([]byte, error) {
	if len(d.b) < n {
		return nil, errBadKey
	}
	p := d.b[:n]
	d.b = d.b[n:]
	return p, nil
}

// cutAhead reports whether the cut mark comes next.
func (d *decoder) cutAhead() bool { return len(d.b) >= 2 && d.b[0] == 0xFF && d.b[1] == 0xFF }

// enter counts one more array or map around what follows.
func (d *decoder) enter() error {
	if d.depth++; d.depth > maxKeyDepth {
		return errBadKey
	}
	return nil
}

func (d *decoder) value() (Value, error) {
	if d.cutAhead() {
		return Value{}, ErrCutKey
	}
	tag, err := d.byte()
	if err != nil {
		return Value{}, err
	}
	switch tag {
	case tagNull:
		return Null(), nil
	case tagMinKey:
		return MinKey(), nil
	case tagMaxKey:
		return MaxKey(), nil
	case tagBoolean:
		c, err := d.byte()
		if err != nil || c > 1 {
			return Value{}, errBadKey
		}
		return Bool(c == 1), nil
	case tagNumber:
		v, err := d.number()
		if d.int32s && v.kind == KindInteger && v.i >= math.MinInt32 && v.i <= math.MaxInt32 {
			v = Int32(int32(v.i))
		}
		return v, err
	case tagTimestamp:
		p, err := d.fixed(12)
		if err != nil {
			return Value{}, err
		}
		return Timestamp(int64(binary.BigEndian.Uint64(p)^1<<63), int32(binary.BigEndian.Uint32(p[8:])^1<<31)), nil
	case tagBSONTimestamp:
		p, err := d.fixed(8)
		if err != nil {
			return Value{}, err
		}
		return BSONTimestampOf(binary.BigEndian.Uint32(p), binary.BigEndian.Uint32(p[4:])), nil
	case tagString:
		s, err := d.text()
		return String(s), err
	case tagBytes:
		s, err := d.text()
		return Value{kind: KindBytes, s: s}, err
	case tagBinary:
		s, err := d.text()
		return bsonMap(BSONBinary, Value{kind: KindBytes, s: s}), err
	case tagReference:
		return d.reference()
	case tagObjectID:
		p, err := d.fixed(12)
		if err != nil {
			return Value{}, err
		}
		return ObjectID([12]byte(p)), nil
	case tagGeoPoint:
		lat, err := d.number()
		if err != nil {
			return Value{}, err
		}
		lng, err := d.number()
		return GeoPoint(lat.DoubleValue(), lng.DoubleValue()), err
	case tagRegex:
		pattern, err := d.text()
		if err != nil {
			return Value{}, err
		}
		if d.cutAhead() {
			return Value{}, ErrCutKey
		}
		options, err := d.text()
		return Regex(pattern, options), err
	case tagArray:
		if err := d.enter(); err != nil {
			return Value{}, err
		}
		var els []Value
		for {
			if len(d.b) > 0 && d.b[0] == end {
				d.b = d.b[1:]
				d.depth--
				return Value{kind: KindArray, list: els}, nil
			}
			x, err := d.value()
			if err != nil {
				return Value{}, err
			}
			els = append(els, x)
		}
	case tagVector:
		p, err := d.fixed(8)
		if err != nil {
			return Value{}, err
		}
		n := binary.BigEndian.Uint64(p)
		var ds []float64
		for range n {
			if d.cutAhead() {
				return Value{}, ErrCutKey
			}
			c, err := d.byte()
			if err != nil || c != item {
				return Value{}, errBadKey
			}
			q, err := d.fixed(8)
			if err != nil {
				return Value{}, err
			}
			u := binary.BigEndian.Uint64(q)
			if u>>63 != 0 {
				u &^= 1 << 63
			} else {
				u = ^u
			}
			ds = append(ds, math.Float64frombits(u))
		}
		return Vector(ds...), nil
	case tagMap:
		if err := d.enter(); err != nil {
			return Value{}, err
		}
		var fs []Field
		for {
			if d.cutAhead() {
				return Value{}, ErrCutKey
			}
			c, err := d.byte()
			if err != nil {
				return Value{}, err
			}
			if c == end {
				d.depth--
				return Value{kind: KindMap, m: fs}, nil
			}
			if c != item {
				return Value{}, errBadKey
			}
			name, err := d.text()
			if err != nil {
				return Value{}, err
			}
			if len(fs) > 0 && fs[len(fs)-1].Name >= name {
				return Value{}, errBadKey
			}
			x, err := d.value()
			if err != nil {
				return Value{}, err
			}
			fs = append(fs, Field{name, x})
		}
	}
	return Value{}, errBadKey
}

// text reads escaped bytes up to their terminator.
func (d *decoder) text() (string, error) {
	var b strings.Builder
	for {
		c, err := d.byte()
		if err != nil {
			return "", err
		}
		if c != 0x00 && c != 0xFF {
			b.WriteByte(c)
			continue
		}
		n, err := d.byte()
		if err != nil {
			return "", err
		}
		switch {
		case c == 0x00 && n == 0x01:
			return b.String(), nil
		case c == 0x00 && n == 0xFF:
			b.WriteByte(0x00)
		case c == 0xFF && n == 0x00:
			b.WriteByte(0xFF)
		case c == 0xFF && n == 0xFF:
			return "", ErrCutKey
		default:
			return "", errBadKey
		}
	}
}

// number reads a number: an integer when it is one within int64, else a double when it is one,
// else a Decimal128. DecodeKey's final re-encoding refuses bits no number has.
func (d *decoder) number() (Value, error) {
	class, err := d.byte()
	if err != nil {
		return Value{}, err
	}
	switch class {
	case classNaN:
		return Double(math.NaN()), nil
	case classNegInf:
		return Double(math.Inf(-1)), nil
	case classPosInf:
		return Double(math.Inf(1)), nil
	case classZero:
		return Int(0), nil
	case classNegative, classPositive:
	default:
		return Value{}, errBadKey
	}
	neg := class == classNegative
	flip := func(c byte) byte {
		if neg {
			return ^c
		}
		return c
	}
	p, err := d.fixed(11)
	if err != nil {
		return Value{}, err
	}
	var mag [11]byte
	for i, c := range p {
		mag[i] = flip(c)
	}
	exp := int(binary.BigEndian.Uint16(mag[:2])) - exponentBias
	frac := binary.BigEndian.Uint64(mag[2:10])
	switch mag[10] {
	case exact:
		return exactNumber(neg, exp, frac)
	case inexact:
	default:
		return Value{}, errBadKey
	}
	q, err := d.fixed(2)
	if err != nil {
		return Value{}, err
	}
	e := int(binary.BigEndian.Uint16([]byte{flip(q[0]), flip(q[1])})) - exponentBias
	var digits []byte
	for {
		c, err := d.byte()
		if err != nil {
			return Value{}, err
		}
		c = flip(c)
		if c == end {
			break
		}
		if c < 1 || c > 100 || len(digits) >= decimalDigits {
			return Value{}, errBadKey
		}
		digits = append(digits, '0'+(c-1)/10, '0'+(c-1)%10)
	}
	if len(digits) == 0 {
		return Value{}, errBadKey
	}
	coef, _ := new(big.Int).SetString(string(digits), 10) // a padded last pair only scales it
	dec, ok := clampDecimal(decimal{neg: neg, coef: coef, exp: e - len(digits)})
	if !ok {
		return Value{}, errBadKey
	}
	return bsonMap(BSONDecimal128, String(dec.String())), nil
}

// exactNumber is the number (1 + frac/2^64) × 2^exp, negated when neg.
func exactNumber(neg bool, exp int, frac uint64) (Value, error) {
	// An integer when no bit lies below 2^0 and it fits int64.
	if exp >= 0 && exp <= 63 && frac<<exp == 0 {
		u := uint64(1)<<exp | frac>>(64-exp)
		switch {
		case !neg && u <= math.MaxInt64:
			return Int(int64(u)), nil
		case neg && u <= 1<<63:
			return Int(int64(-u)), nil
		}
	}
	// A double when one holds it: 53 significant bits, fewer below 2^-1022, and the exponent in
	// range. Ldexp rounds what does not fit, which the re-encoding shows.
	if frac<<52 == 0 && exp >= -1074 && exp <= 1023 {
		f := math.Ldexp(float64(1<<52|frac>>12), exp-52)
		if g := appendNumber(nil, number{f: f}); bytes.Equal(g[1:], append(appendMagnitude(nil, exp, frac), exact)) {
			if neg {
				f = -f
			}
			return Double(f), nil
		}
	}
	// Else a decimal: (2^64 + frac) × 2^(exp-64), written out in decimal.
	m := new(big.Int).Add(two64, new(big.Int).SetUint64(frac))
	e := 0
	if shift := exp - 64; shift >= 0 {
		m.Lsh(m, uint(shift))
	} else {
		m.Mul(m, new(big.Int).Exp(big.NewInt(5), big.NewInt(int64(-shift)), nil))
		e = shift
	}
	dec, ok := clampDecimal(decimal{neg: neg, coef: m, exp: e})
	if !ok {
		return Value{}, errBadKey
	}
	return bsonMap(BSONDecimal128, String(dec.String())), nil
}

func (d *decoder) reference() (Value, error) {
	project, err := d.text()
	if err != nil {
		return Value{}, err
	}
	database, err := d.text()
	if err != nil {
		return Value{}, err
	}
	var segs []string
	for {
		if d.cutAhead() {
			return Value{}, ErrCutKey
		}
		c, err := d.byte()
		if err != nil {
			return Value{}, err
		}
		switch c {
		case end:
			if project == "" && database == "" && len(segs) == 1 {
				return Reference(segs[0]), nil
			}
			return Reference("projects/" + project + "/databases/" + database + "/documents/" + strings.Join(segs, "/")), nil
		case item:
			if d.cutAhead() {
				return Value{}, ErrCutKey
			}
			p, err := d.fixed(8)
			if err != nil {
				return Value{}, err
			}
			segs = append(segs, "__id"+strconv.FormatInt(int64(binary.BigEndian.Uint64(p)^1<<63), 10)+"__")
		case segText:
			s, err := d.text()
			if err != nil {
				return Value{}, err
			}
			segs = append(segs, s)
		default:
			return Value{}, errBadKey
		}
	}
}
