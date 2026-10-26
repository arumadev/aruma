package value

import (
	"errors"
	"math/big"
	"strconv"
	"strings"
)

// BSONKind names the Enterprise BSON type a map stands for. The SDKs (JS 4.18, Android 27,
// iOS 13) send these types as one-field maps, and production's Standard edition accepts them
// and orders them as the types they name (values-bson.json):
//
//	MinKey      {__min__: null}
//	MaxKey      {__max__: null}
//	Int32       {__int__: integer in int32}
//	Decimal128  {__decimal128__: string}, a decimal of at most 34 digits
//	Timestamp   {__request_timestamp__: {seconds: uint32, increment: uint32}}
//	Binary      {__binary__: bytes}, the subtype byte (not 0) then the data
//	ObjectID    {__oid__: 24 lowercase hex digits}
//	Regex       {__regex__: {pattern: string, options: string}}
//
// Like vectors they stay maps in Value, so the wire round trip, Identical and the field mask
// syntax see maps; Compare, Key, Size, Equal and Validate see the type.
type BSONKind uint8

const (
	NotBSON BSONKind = iota
	BSONMinKey
	BSONMaxKey
	BSONInt32
	BSONDecimal128
	BSONTimestamp
	BSONBinary
	BSONObjectID
	BSONRegex
)

// bsonKeys are the map keys of the BSON shapes, by kind.
var bsonKeys = [...]string{"", "__min__", "__max__", "__int__", "__decimal128__", "__request_timestamp__", "__binary__", "__oid__", "__regex__"}

func (k BSONKind) String() string {
	if k == NotBSON || int(k) >= len(bsonKeys) {
		return "not BSON"
	}
	return strings.Trim(bsonKeys[k], "_")
}

// bsonKeyKind returns the kind a map key names, NotBSON for any other key.
func bsonKeyKind(name string) BSONKind {
	if !strings.HasPrefix(name, "__") {
		return NotBSON
	}
	for k := BSONMinKey; k <= BSONRegex; k++ {
		if bsonKeys[k] == name {
			return k
		}
	}
	return NotBSON
}

// bson is a BSON shape's content, parsed.
type bson struct {
	kind BSONKind
	i    int64   // Int32's value
	ts   uint64  // Timestamp's seconds<<32 | increment
	dec  decimal // Decimal128
	s    string  // Binary's bytes; ObjectID's 12 bytes; Regex's pattern
	o    string  // Regex's options
}

// bsonOf parses v as a BSON shape: a map of exactly one field named by a BSON key, whose
// content production accepts. A map whose content production refuses is not one; Validate
// refuses it, and Compare and Key, which must order every value, treat it as a plain map.
func bsonOf(v Value) (bson, bool) {
	b, err := parseBSON(v)
	return b, err == nil && b.kind != NotBSON
}

// BSONKind returns the BSON type v stands for, NotBSON when v is no valid BSON shape.
func (v Value) BSONKind() BSONKind {
	if b, ok := bsonOf(v); ok {
		return b.kind
	}
	return NotBSON
}

// parseBSON parses a one-field map named by a BSON key, and returns production's message for
// content it refuses (values-bson.json, bad-*). Any other value is NotBSON, with no error.
// Extra keys inside a timestamp's or a regex's map are accepted and dropped (bad-bts-extra,
// bad-re-extra: stored without them); CanonicalBSON drops them.
func parseBSON(v Value) (bson, error) {
	if v.kind != KindMap || len(v.m) != 1 {
		return bson{}, nil
	}
	f := v.m[0]
	b := bson{kind: bsonKeyKind(f.Name)}
	x := f.Value
	switch b.kind {
	case NotBSON:
		return bson{}, nil
	case BSONMinKey, BSONMaxKey:
		if x.kind != KindNull {
			return b, errors.New("Field " + f.Name + " must be null; founds " + datastoreType(x) + ".")
		}
	case BSONInt32:
		if x.kind != KindInteger {
			return b, errors.New("Field __int__ must be a long; founds " + datastoreType(x) + ".")
		}
		if x.i < -1<<31 || x.i >= 1<<31 {
			return b, errors.New("The field '__int__' value (" + javaGrouped(x.i) + ") is too large to be converted to a 32-bit integer.")
		}
		b.i = x.i
	case BSONDecimal128:
		if x.kind != KindString {
			return b, errors.New("Field __decimal128__ must be a string; founds " + datastoreType(x) + ".")
		}
		d, ok := parseDecimal128(x.s)
		if !ok {
			return b, errors.New("Invalid decimal128 string")
		}
		b.dec = d
	case BSONTimestamp:
		if x.kind != KindMap {
			return b, errors.New("Field __request_timestamp__ must be a map; founds " + datastoreType(x) + ".")
		}
		var parts [2]uint64
		for i, key := range [2]string{"seconds", "increment"} {
			y, ok := x.Get(key)
			switch {
			case !ok:
				return b, errors.New(`__request_timestamp__ map is missing value for key "` + key + `".`)
			case y.kind != KindInteger:
				return b, errors.New(`__request_timestamp__ map key "` + key + `" value must be an integer; found ` + datastoreType(y) + ".")
			case y.i < 0 || y.i > 1<<32-1:
				return b, errors.New("The field '" + key + "' value (" + javaGrouped(y.i) + ") does not represent an unsigned 32-bit integer.")
			}
			parts[i] = uint64(y.i)
		}
		b.ts = parts[0]<<32 | parts[1]
	case BSONBinary:
		switch {
		case x.kind != KindBytes:
			return b, errors.New("Field __binary__ must contain bytes; founds " + datastoreType(x) + ".")
		case x.s == "":
			return b, errors.New("BytesWithSubtype cannot be empty.")
		case x.s[0] == 0:
			return b, errors.New("Invalid BytesWithSubtype: subtype 0 is not permitted. Use the standard bytes type instead.")
		}
		b.s = x.s
	case BSONObjectID:
		if x.kind != KindString {
			return b, errors.New("Field __oid__ must be a string; founds " + datastoreType(x) + ".")
		}
		if len(x.s) != 24 {
			return b, errors.New("Object ID hex string has incorrect length.")
		}
		raw := make([]byte, 12)
		for i := range raw {
			hi, ok1 := lowerHex(x.s[2*i])
			lo, ok2 := lowerHex(x.s[2*i+1])
			if !ok1 || !ok2 {
				// Uppercase hex is refused too (oid-507f-upper).
				return b, errors.New("Invalid object ID hex string.")
			}
			raw[i] = hi<<4 | lo
		}
		b.s = string(raw)
	case BSONRegex:
		if x.kind != KindMap {
			return b, errors.New("Field __regex__ must be a map; founds " + datastoreType(x) + ".")
		}
		var parts [2]string
		for i, key := range [2]string{"pattern", "options"} {
			y, ok := x.Get(key)
			switch {
			case !ok:
				return b, errors.New(`__regex__ map is missing value for key "` + key + `".`)
			case y.kind != KindString:
				return b, errors.New(`__regex__ map key "` + key + `" value must be a string; found ` + datastoreType(y) + ".")
			}
			parts[i] = y.s
		}
		if err := checkRegexOptions(parts[1]); err != nil {
			return b, err
		}
		// The pattern is not compiled: "(" is accepted (re-paren).
		b.s, b.o = parts[0], parts[1]
	}
	return b, nil
}

func lowerHex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

// checkRegexOptions checks a regex's options, character by character: each one of imsux, none
// twice, in order (re-a-z, re-a-zi, re-a-ii, re-a-mi).
func checkRegexOptions(o string) error {
	for i := 0; i < len(o); i++ {
		c := o[i]
		switch {
		case !strings.ContainsRune("imsux", rune(c)):
			return errors.New("Invalid regex option '" + o[i:i+1] + "'. Supported options are 'i', 'm', 's', 'u', and 'x'.")
		case strings.ContainsRune(o[:i], rune(c)):
			return errors.New("Duplicate regex option '" + o[i:i+1] + "'.")
		case i > 0 && c < o[i-1]:
			return errors.New("Regex options must be sorted.")
		}
	}
	return nil
}

// javaGrouped formats n as Java's %,d: 2,147,483,648 and -1.
func javaGrouped(n int64) string {
	s := strconv.FormatInt(n, 10)
	digits, sign := strings.CutPrefix(s, "-")
	var b strings.Builder
	if sign {
		b.WriteByte('-')
	}
	for i := range len(digits) {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteByte(digits[i])
	}
	return b.String()
}

// The BSON constructors, each the map the SDKs send.

// MinKey returns BSON MinKey, which sorts after null and before every other value.
func MinKey() Value { return bsonMap(BSONMinKey, Null()) }

// MaxKey returns BSON MaxKey, which sorts after every other value.
func MaxKey() Value { return bsonMap(BSONMaxKey, Null()) }

// Int32 returns a BSON int32, a number equal to the integer of its value.
func Int32(i int32) Value { return bsonMap(BSONInt32, Int(int64(i))) }

// Decimal128 returns a BSON Decimal128 of a decimal string (1.5, -1E+308, NaN, Infinity), in
// the canonical form production stores; ok is false for a string production refuses.
func Decimal128(s string) (Value, bool) {
	d, ok := parseDecimal128(s)
	if !ok {
		return Value{}, false
	}
	return bsonMap(BSONDecimal128, String(d.String())), true
}

// BSONTimestampOf returns a BSON timestamp, which sorts after every timestamp, by seconds then
// increment.
func BSONTimestampOf(seconds, increment uint32) Value {
	return bsonMap(BSONTimestamp, Map(Field{"increment", Int(int64(increment))}, Field{"seconds", Int(int64(seconds))}))
}

// BSONBinaryOf returns BSON binary data of a subtype (not 0, which production refuses) and data.
func BSONBinaryOf(subtype byte, data []byte) Value {
	return bsonMap(BSONBinary, Value{kind: KindBytes, s: string(append([]byte{subtype}, data...))})
}

// ObjectID returns a BSON ObjectId of 12 bytes.
func ObjectID(id [12]byte) Value {
	const hex = "0123456789abcdef"
	var b [24]byte
	for i, c := range id {
		b[2*i], b[2*i+1] = hex[c>>4], hex[c&15]
	}
	return bsonMap(BSONObjectID, String(string(b[:])))
}

// Regex returns a BSON regular expression; options must be sorted letters of imsux.
func Regex(pattern, options string) Value {
	return bsonMap(BSONRegex, Map(Field{"options", String(options)}, Field{"pattern", String(pattern)}))
}

func bsonMap(k BSONKind, x Value) Value {
	return Value{kind: KindMap, m: []Field{{bsonKeys[k], x}}}
}

// CanonicalBSON returns v, a document's fields or any value, with every BSON shape in it as
// production stores it: a Decimal128 in canonical form (+1.5 and 15e-1 as 1.5, 01.50 as 1.50,
// inf as Infinity, 1E+6144 as 1.000000000000000000000000000000000E+6144), and a timestamp's or
// regex's map without extra keys. Timestamps are TruncateMicros's concern.
func CanonicalBSON(v Value) Value {
	switch v.kind {
	case KindArray:
		var out []Value
		for i, e := range v.list {
			c := CanonicalBSON(e)
			if out == nil && !Identical(c, e) {
				out = make([]Value, len(v.list))
				copy(out, v.list[:i])
			}
			if out != nil {
				out[i] = c
			}
		}
		if out != nil {
			return Value{kind: KindArray, list: out}
		}
	case KindMap:
		if b, ok := bsonOf(v); ok {
			switch b.kind {
			case BSONDecimal128:
				return bsonMap(BSONDecimal128, String(b.dec.String()))
			case BSONTimestamp:
				return BSONTimestampOf(uint32(b.ts>>32), uint32(b.ts))
			case BSONRegex:
				return Regex(b.s, b.o)
			}
			return v
		}
		var out []Field
		for i, f := range v.m {
			c := CanonicalBSON(f.Value)
			if out == nil && !Identical(c, f.Value) {
				out = make([]Field, len(v.m))
				copy(out, v.m[:i])
			}
			if out != nil {
				out[i] = Field{f.Name, c}
			}
		}
		if out != nil {
			return Value{kind: KindMap, m: out}
		}
	}
	return v
}

// ---- Decimal128 ----

// The Decimal128 limits: 34 digits, and value = coefficient × 10^exponent with the exponent in
// [minDecimalExponent, maxDecimalExponent].
const (
	decimalDigits      = 34
	maxDecimalExponent = 6111
	minDecimalExponent = -6176
)

type decimalForm uint8

const (
	decFinite decimalForm = iota
	decNaN
	decInf
)

// decimal is a Decimal128: neg and form, and for a finite one coef × 10^exp (coef ≥ 0, at most
// 34 digits). -0 keeps its sign, which only the string shows: it equals 0.
type decimal struct {
	form decimalForm
	neg  bool
	coef *big.Int
	exp  int
}

var bigTen = big.NewInt(10)

func pow10(n int) *big.Int { return new(big.Int).Exp(bigTen, big.NewInt(int64(n)), nil) }

// parseDecimal128 parses a decimal string as production does, which is the MongoDB Java
// driver's Decimal128.parse: NaN, -NaN, Inf, +Inf, Infinity, +Infinity, -Inf and -Infinity in
// any case; otherwise Java's BigDecimal syntax (an optional sign, digits with an optional
// point, an optional exponent; no space), with the value held exactly: more than 34 digits, or
// an exponent out of range, is refused unless only zeros are dropped or added to fit
// (values-bson.json, dec-*). Java's BigDecimal also reads non-ASCII digits; this reads ASCII.
func parseDecimal128(s string) (decimal, bool) {
	switch strings.ToLower(s) {
	case "nan":
		return decimal{form: decNaN}, true
	case "-nan":
		return decimal{form: decNaN, neg: true}, true
	case "inf", "+inf", "infinity", "+infinity":
		return decimal{form: decInf}, true
	case "-inf", "-infinity":
		return decimal{form: decInf, neg: true}, true
	}
	rest := s
	neg := strings.HasPrefix(rest, "-")
	if neg || strings.HasPrefix(rest, "+") {
		rest = rest[1:]
	}
	mantissa, exponent, hasExp := strings.Cut(strings.ReplaceAll(rest, "E", "e"), "e")
	whole, fraction, _ := strings.Cut(mantissa, ".")
	if whole+fraction == "" || !allDigits(whole) || !allDigits(fraction) {
		return decimal{}, false
	}
	exp := int64(0)
	if hasExp {
		sign := int64(1)
		switch {
		case strings.HasPrefix(exponent, "-"):
			sign, exponent = -1, exponent[1:]
		case strings.HasPrefix(exponent, "+"):
			exponent = exponent[1:]
		}
		e, err := strconv.ParseInt(exponent, 10, 64)
		if exponent == "" || !allDigits(exponent) || err != nil || sign*e < -1<<31 || sign*e >= 1<<31 {
			return decimal{}, false
		}
		exp = sign * e
	}
	// Java's scale, fraction digits less the exponent, must fit an int.
	scale := int64(len(fraction)) - exp
	if scale < -1<<31 || scale >= 1<<31 {
		return decimal{}, false
	}
	coef, _ := new(big.Int).SetString(whole+fraction, 10)
	return clampDecimal(decimal{neg: neg, coef: coef, exp: int(-scale)})
}

func allDigits(s string) bool {
	return strings.Trim(s, "0123456789") == ""
}

// clampDecimal fits a parsed decimal to Decimal128 without rounding: the MongoDB driver's
// clampAndRound, which pads the coefficient with zeros for an exponent above the range and drops
// trailing zeros for one below it or for more than 34 digits, and refuses anything else.
func clampDecimal(d decimal) (decimal, bool) {
	sig := d.coef.String()
	precision := len(sig)
	switch {
	case d.exp > maxDecimalExponent:
		if d.coef.Sign() == 0 {
			d.exp = maxDecimalExponent
			break
		}
		diff := d.exp - maxDecimalExponent
		if diff+precision > decimalDigits {
			return decimal{}, false
		}
		d.coef = new(big.Int).Mul(d.coef, pow10(diff))
		d.exp = maxDecimalExponent
	case d.exp < minDecimalExponent:
		diff := minDecimalExponent - d.exp
		kept := max(0, precision-diff)
		if strings.Trim(sig[kept:], "0") != "" {
			return decimal{}, false
		}
		if kept > 0 {
			d.coef = new(big.Int).Quo(d.coef, pow10(diff))
		}
		d.exp = minDecimalExponent
	case precision > decimalDigits:
		extra := precision - decimalDigits
		if strings.Trim(sig[decimalDigits:], "0") != "" {
			return decimal{}, false
		}
		d.coef = new(big.Int).Quo(d.coef, pow10(extra))
		d.exp += extra
	}
	// The driver's own range checks, which an exponent padded or trimmed above can still fail.
	if d.exp < minDecimalExponent || d.exp > maxDecimalExponent || d.coef.BitLen() > 113 {
		return decimal{}, false
	}
	return d, true
}

// String formats d as production returns it, the MongoDB driver's Decimal128.toString: plain
// notation when the exponent is at most 0 and the adjusted exponent at least -6 (1.50, 0.000001,
// -0), else scientific (1E+2, 1E-7, 1.234E+34, 0E-6176).
func (d decimal) String() string {
	switch d.form {
	case decNaN:
		return "NaN"
	case decInf:
		if d.neg {
			return "-Infinity"
		}
		return "Infinity"
	}
	var b strings.Builder
	if d.neg {
		b.WriteByte('-')
	}
	sig := d.coef.String()
	adjusted := d.exp + len(sig) - 1
	switch {
	case d.exp == 0:
		b.WriteString(sig)
	case d.exp < 0 && adjusted >= -6:
		if pad := -d.exp - len(sig); pad >= 0 {
			b.WriteString("0.")
			b.WriteString(strings.Repeat("0", pad))
			b.WriteString(sig)
		} else {
			b.WriteString(sig[:-pad])
			b.WriteByte('.')
			b.WriteString(sig[-pad:])
		}
	default:
		b.WriteString(sig[:1])
		if len(sig) > 1 {
			b.WriteByte('.')
			b.WriteString(sig[1:])
		}
		b.WriteByte('E')
		if adjusted > 0 {
			b.WriteByte('+')
		}
		b.WriteString(strconv.Itoa(adjusted))
	}
	return b.String()
}

// rat returns a finite decimal's exact value.
func (d decimal) rat() *big.Rat {
	r := new(big.Rat)
	if d.exp >= 0 {
		r.SetInt(new(big.Int).Mul(d.coef, pow10(d.exp)))
	} else {
		r.SetFrac(d.coef, pow10(-d.exp))
	}
	if d.neg {
		r.Neg(r)
	}
	return r
}

// digits returns a finite nonzero decimal's significant digits without trailing zeros and its
// adjusted exponent e, with |d| = 0.digits × 10^e.
func (d decimal) digits() (string, int) {
	sig := d.coef.String()
	trimmed := strings.TrimRight(sig, "0")
	return trimmed, d.exp + len(sig)
}
