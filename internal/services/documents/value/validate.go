package value

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// The limits Validate holds a document's fields to (values-validate.json).
const (
	// MaxDepth is how deep maps and arrays may nest: 20 maps around a value pass, 21 fail. A
	// map counts 1, empty or not; an array counts 1 when it holds anything, and an empty array
	// and a vector count nothing (20 maps around [] or a vector pass).
	MaxDepth = 20
	// MaxFieldNameBytes bounds a field name, and the dotted path of a nested one.
	MaxFieldNameBytes = 1500
	// MaxVectorDimensions is the most elements a vector may hold.
	MaxVectorDimensions = 2048
)

// Validate checks a document's fields (a map value) as production checks a write, and returns
// its INVALID_ARGUMENT message for the first problem found:
//
//   - a field name may not be empty or longer than 1500 bytes, nor be reserved (__x__);
//     within a map the dotted path from the document (a.b.c, unquoted) may not pass 1500 bytes;
//   - maps and arrays nest at most MaxDepth deep;
//   - a string or bytes value holds at most MaxStringBytes;
//   - a map holding __type__ must be a vector: {__type__: "__vector__", value: [1 to 2048
//     doubles, none NaN]};
//   - a map of one field named by a BSON key (BSONKind) must hold what that type does, with
//     production's messages (values-bson.json, bad-*); a map of more fields may not use such a
//     key, which is reserved;
//   - a geopoint's latitude lies in [-90, 90] and its longitude in [-180, 180].
//
// Production names the culprit in two ways: a problem inside a map is reported as "Property p
// contains an invalid nested entity." where p is the top-level field holding the outermost map
// on the way, or "array" when that map sits in an array; a reserved name keeps its own message
// inside a map. Vectors, BSON shapes and geopoints keep theirs too (a BSON shape's recorded in a
// map and in an array as well). A vector or BSON shape adds nothing to the depth: 20 maps around
// one pass (depth-20-maps-int32). Nested arrays ([[1]]), which the docs forbid, are accepted, as
// production does. Strings and bytes longer than the index keeps (1500 bytes, see Compare) are
// accepted: the index cuts them.
//
// Not checked here: the document size (DocumentSize), timestamps out of range and malformed
// references and bytes, which the wire refuses while parsing. A BSON shape production accepts
// with extra keys inside (a timestamp's or a regex's map) is stored without them: CanonicalBSON.
// A map with two reserved keys is reported by the first in byte order; production names the
// first in the request's order ({__min__, __max__} as __min__), which a Value does not keep.
//
// A malformed BSON shape is reported before anything else (CheckBSON), as production finds it
// while it reads the request. So an update with a mask is checked in this order
// (values-mask-vector.json): CheckBSON on the update's fields ({b: {__int__: "x"}} under the
// mask b.__int__ gets the BSON message), ValidateMaskPath on each path (vec.__type__ under a
// stray {vec: {__type__: "x"}} gets the mask's), then Validate and DocumentSize on the document
// ApplyMask makes, whose depth and size the mask can change.
func Validate(fields Value) error {
	if err := CheckBSON(fields); err != nil {
		return err
	}
	return validateFields(fields.Fields())
}

// ValidateChanged returns what Validate returns for a document of n fields whose other fields
// passed Validate, in this document or another: changed holds its fields that are new or
// changed, in name order. A field is checked alone but for one thing, a document of a single
// field named by a BSON key, which CheckBSON reads as that shape; a field that passed is never
// so named, since the name is reserved. So a write that changes a few fields of a large
// document costs those fields (F2 re-review N1).
func ValidateChanged(changed []Field, n int) error {
	if n == 1 && len(changed) == 1 {
		return Validate(Value{kind: KindMap, m: changed})
	}
	for _, f := range changed {
		if err := CheckBSON(f.Value); err != nil {
			return err
		}
	}
	return validateFields(changed)
}

// validateFields checks fields, once CheckBSON passed them, in order.
func validateFields(fields []Field) error {
	for _, f := range fields {
		switch {
		case f.Name == "":
			return errors.New("The property.name is the empty string.")
		case len(f.Name) > MaxFieldNameBytes:
			return errors.New("The property.name is longer than 1500 bytes.")
		case reserved(f.Name):
			return errors.New("field name " + f.Name + " is reserved")
		}
		w := walker{property: f.Name}
		if err := w.value(f.Value, f.Name, 0); err != nil {
			return err
		}
	}
	return nil
}

// reserved reports whether a field name is reserved: two underscores, at least one character,
// two underscores. The docs say __.*__, but production accepts "____" (and "___", "__a_").
func reserved(name string) bool {
	return len(name) >= 5 && strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__")
}

// walker validates one top-level field's value. property names the field outside any map: its
// own name at the top, "array" within an array. entity is set once the walk has entered a map,
// after which a problem is reported as an invalid nested entity of entityName.
type walker struct {
	property   string
	entity     bool
	entityName string
}

// nested is a problem found inside a map, in production's words.
func (w *walker) nested() error {
	return errors.New("Property " + w.entityName + " contains an invalid nested entity.")
}

// value checks v, found at the dotted path path, depth containers deep.
func (w *walker) value(v Value, path string, depth int) error {
	switch v.kind {
	case KindString, KindBytes:
		if len(v.s) > MaxStringBytes {
			if w.entity {
				return w.nested()
			}
			return errors.New(`The value of property "` + w.property + `" is longer than 1048487 bytes.`)
		}
	case KindGeoPoint:
		if !(v.f >= -90 && v.f <= 90) {
			return errors.New("Geo point latitude '" + javaDouble(v.f) + "' outside permitted range -90.0 to 90.0.")
		}
		if !(v.g >= -180 && v.g <= 180) {
			return errors.New("Geo point longitude '" + javaDouble(v.g) + "' outside permitted range -180.0 to 180.0.")
		}
	case KindArray:
		if len(v.list) == 0 {
			return nil
		}
		if depth+1 > MaxDepth {
			return w.tooDeep()
		}
		inner := *w
		if !inner.entity {
			inner.property = "array"
		}
		for _, e := range v.list {
			if err := inner.value(e, path, depth+1); err != nil {
				return err
			}
		}
	case KindMap:
		return w.mapValue(v, path, depth)
	}
	return nil
}

func (w *walker) tooDeep() error {
	if w.entity {
		return w.nested()
	}
	return errors.New("At most 20 nested array/entity values are supported.")
}

func (w *walker) mapValue(v Value, path string, depth int) error {
	if _, ok := v.Get(vectorTypeKey); ok {
		return w.vector(v)
	}
	if b, err := parseBSON(v); b.kind != NotBSON {
		return err
	}
	inner := *w
	if !inner.entity {
		inner.entity, inner.entityName = true, w.property
	}
	if depth+1 > MaxDepth {
		return inner.nested()
	}
	for _, f := range v.m {
		sub := path + "." + f.Name
		switch {
		case f.Name == "" || len(sub) > MaxFieldNameBytes:
			return inner.nested()
		case reserved(f.Name):
			return errors.New("field name '" + f.Name + "' is reserved.")
		}
		if err := inner.value(f.Value, sub, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// vector checks a map holding __type__, which only a vector may (values-validate.json,
// vector.* and type.*).
func (w *walker) vector(v Value) error {
	t, _ := v.Get(vectorTypeKey)
	if t.kind != KindString {
		return errors.New("Field __type__ must be a string; founds " + datastoreType(t) + ".")
	}
	if t.s != vectorTypeValue {
		inner := *w
		if !inner.entity {
			inner.entityName = w.property
		}
		return inner.nested()
	}
	arr, ok := v.Get(vectorValueKey)
	switch {
	case !ok:
		return errors.New("Vector map is missing key 'value'.")
	case arr.kind != KindArray:
		return errors.New("Vector value type must be an array.")
	case len(v.m) != 2:
		return errors.New("Vector value map has extraneous entries.")
	}
	for _, e := range arr.list {
		if e.kind != KindDouble {
			return errors.New("Vector must only contain values of type 'double'")
		}
	}
	for _, e := range arr.list {
		if math.IsNaN(e.f) {
			return errors.New("Vector cannot contain NaN values.")
		}
	}
	switch {
	case len(arr.list) == 0:
		return errors.New("Cannot have a zero length vector.")
	case len(arr.list) > MaxVectorDimensions:
		return errors.New("Vectors must be at most 2048 dimensions.")
	}
	return nil
}

// datastoreType names a value's type as production's messages do (vector.type_*).
func datastoreType(v Value) string {
	switch v.kind {
	case KindInteger:
		return "LONG"
	case KindReference:
		return "ENTITY_REF"
	case KindGeoPoint:
		return "GEO_POINT"
	}
	return strings.ToUpper(v.kind.String())
}

// javaDouble formats f as Java's Double.toString, which production's geopoint messages use:
// 91.0, -90.5, NaN, Infinity, 1.0E10, 1.0E-5.
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
	if a := math.Abs(f); a >= 1e-3 && a < 1e7 {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	}
	s := strconv.FormatFloat(f, 'e', -1, 64) // 1e+10, 1.5e-05
	mant, exp, _ := strings.Cut(s, "e")
	if !strings.Contains(mant, ".") {
		mant += ".0"
	}
	n, _ := strconv.Atoi(exp)
	return mant + "E" + strconv.Itoa(n)
}

// CheckBSON returns production's message for the first malformed BSON shape in v (a one-field
// map named by a BSON key whose content production refuses), in field and element order, or nil.
// Production finds these while it reads a request, before it checks a mask or anything else
// Validate does.
func CheckBSON(v Value) error {
	switch v.kind {
	case KindArray:
		for _, e := range v.list {
			if err := CheckBSON(e); err != nil {
				return err
			}
		}
	case KindMap:
		if b, err := parseBSON(v); b.kind != NotBSON {
			return err
		}
		for _, f := range v.m {
			if err := CheckBSON(f.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateOperand checks a value a query compares with (a filter's operand, an element of in's
// array, a cursor value) as production does before it runs the query: vectors and BSON shapes as
// Validate checks them, and no reserved key in any other map (values-equality.json: a range on
// vector [] is refused with "Cannot have a zero length vector."; values-bson.json: binary
// subtype 0, {__int__: "x"}, {__int__: 1, b: 2} and {__bson__: 1}). Its depth and size are not
// checked here: IndexedWhole is the size rule for an operand.
func ValidateOperand(v Value) error {
	switch v.kind {
	case KindArray:
		for _, e := range v.list {
			if err := ValidateOperand(e); err != nil {
				return err
			}
		}
	case KindMap:
		if _, ok := v.Get(vectorTypeKey); ok {
			w := walker{property: "v"}
			return w.vector(v)
		}
		if b, err := parseBSON(v); b.kind != NotBSON {
			return err
		}
		for _, f := range v.m {
			if reserved(f.Name) {
				return errors.New("field name '" + f.Name + "' is reserved.")
			}
			if err := ValidateOperand(f.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateMaskPath checks a parsed path of a read mask or an update mask as production does
// (values-mask-vector.json): no segment may be reserved, so a mask cannot reach into a vector or
// a BSON shape. __type__ has its own message, __name__ below the top another (it names
// __key__, Datastore's word), and any other reserved segment is named with the rest of the path
// from it (b.__int__ as __int__, __min__.a as __min__.a). __name__ alone, the document's name,
// passes.
func ValidateMaskPath(p FieldPath) error {
	if p.IsDocumentID() {
		return nil
	}
	for i, seg := range p {
		switch {
		case seg == vectorTypeKey:
			return errors.New("the name __type__ is reserved")
		case seg == DocumentID:
			return errors.New("the name __key__ is reserved")
		case reserved(seg):
			return errors.New("Invalid reserved name in field path " + p[i:].String())
		}
	}
	return nil
}
