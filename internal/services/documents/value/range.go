package value

// Class is a value's type class for range filters (<, <=, >, >=): production brackets a range
// by its operand's class, so v > 0 returns numbers only, v > "" strings only and v > {} maps
// without vectors or MaxKey (values-equality.json and values-bson.json, the range filters).
// Classes follow the type order (see Compare), one per type, except that bytes and BSON binary
// share one: v > bytes "" returns binary values too, and v < binary 01 02 returns bytes.
type Class uint8

const (
	ClassNull Class = iota
	ClassMinKey
	ClassBoolean
	ClassNumber
	ClassTimestamp
	ClassBSONTimestamp
	ClassString
	ClassBytes // bytes and BSON binary
	ClassReference
	ClassObjectID
	ClassGeoPoint
	ClassRegex
	ClassArray
	ClassVector
	ClassMap
	ClassMaxKey
)

var rankClasses = [...]Class{rankNull: ClassNull, rankMinKey: ClassMinKey, rankBoolean: ClassBoolean, rankNumber: ClassNumber,
	rankTimestamp: ClassTimestamp, rankBSONTimestamp: ClassBSONTimestamp, rankString: ClassString, rankBytes: ClassBytes,
	rankBinary: ClassBytes, rankReference: ClassReference, rankObjectID: ClassObjectID, rankGeoPoint: ClassGeoPoint,
	rankRegex: ClassRegex, rankArray: ClassArray, rankVector: ClassVector, rankMap: ClassMap, rankMaxKey: ClassMaxKey}

// ClassOf returns v's range class.
func ClassOf(v Value) Class { return rankClasses[rank(v)] }

// SameClass reports whether a and b are in one range class.
func SameClass(a, b Value) bool { return ClassOf(a) == ClassOf(b) }

// RangeOp is a range filter's operator.
type RangeOp uint8

const (
	Less RangeOp = iota + 1
	LessOrEqual
	Greater
	GreaterOrEqual
)

// InRange reports whether a stored value w matches the range filter w op x, as production
// matches it: w and x are in one class, neither is null or NaN, and Compare(w, x) satisfies op.
// So v < Infinity leaves out NaN, v > NaN and v > null match nothing, and v >= MinKey matches
// MinKey alone (the 60 recorded range filters of values-equality.json and those of
// values-bson.json).
func InRange(op RangeOp, w, x Value) bool {
	if x.IsNull() || x.IsNaN() || w.IsNaN() || !SameClass(w, x) {
		return false
	}
	r := Compare(w, x)
	switch op {
	case Less:
		return r < 0
	case LessOrEqual:
		return r <= 0
	case Greater:
		return r > 0
	case GreaterOrEqual:
		return r >= 0
	}
	return false
}

// ClassBounds returns the key range [lo, hi) that holds every value of x's class but NaN, so a
// store scans a range filter's candidates as (Key(x), hi) for >, [Key(x), hi) for >=, [lo,
// Key(x)) for < and [lo, Key(x)] for <=, then keeps those InRange accepts (a key the index
// budget cut may tie with x where the value does not). ok is false for an operand that matches
// nothing: null or NaN.
func ClassBounds(x Value) (lo, hi []byte, ok bool) {
	if x.IsNull() || x.IsNaN() {
		return nil, nil, false
	}
	switch ClassOf(x) {
	case ClassNumber:
		// Past NaN, whose key is the number tag and classNaN.
		return []byte{KeyVersion, tagNumber, classNegInf}, []byte{KeyVersion, tagNumber + 1}, true
	case ClassBytes:
		return []byte{KeyVersion, tagBytes}, []byte{KeyVersion, tagBinary + 1}, true
	}
	tag := tags[rank(x)]
	return []byte{KeyVersion, tag}, []byte{KeyVersion, tag + 1}, true
}
