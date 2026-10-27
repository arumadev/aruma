package query

import (
	"slices"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/value"
)

// Doc is a candidate document: its full resource name, which __name__ filters and orders read
// as a reference, its fields (a map value) and its times.
type Doc struct {
	Name       string
	Fields     value.Value
	CreateTime time.Time
	UpdateTime time.Time
}

// field is the value of p in d: __name__ is the document's name as a reference. ok is false
// when d has no such field.
func (d *Doc) field(p value.FieldPath) (value.Value, bool) {
	if p.IsDocumentID() {
		return value.Reference(d.Name), true
	}
	return d.Fields.Lookup(p)
}

// Matches reports whether d matches f, as production matches (values-equality.json,
// values-bson.json, replayed in value's tests, and indexes-105.json's results):
//
//   - a document without the field matches no filter on it, != and not-in and IS_NOT_NULL
//     included;
//   - == and in match by value.Equal, which null and NaN never satisfy: IS_NULL and IS_NAN are
//     how they are found;
//   - a range matches within its operand's type class (value.InRange);
//   - != and not-in leave out null, and an operand holding null matches nothing;
//   - IS_NOT_NAN leaves out null as well as NaN;
//   - array-contains matches an element by value.Equal, except that array-contains null (or
//     NaN) matches the arrays holding an empty array, and nothing else (tr-empty-arrays-*);
//     array-contains-any is any of its values' array-contains.
func (f *Filter) Matches(d *Doc) bool {
	switch f.Op {
	case index.OpAnd:
		for i := range f.Filters {
			if !f.Filters[i].Matches(d) {
				return false
			}
		}
		return true
	case index.OpOr:
		for i := range f.Filters {
			if f.Filters[i].Matches(d) {
				return true
			}
		}
		return false
	}
	w, ok := d.field(f.Path)
	if !ok {
		return false
	}
	return f.matchesValue(w)
}

func (f *Filter) matchesValue(w value.Value) bool {
	switch f.Op {
	case index.OpEqual:
		return value.Equal(w, f.Value)
	case index.OpIn:
		return slices.ContainsFunc(f.Values, func(x value.Value) bool { return value.Equal(w, x) })
	case index.OpNotEqual:
		return !f.Value.IsNull() && !w.IsNull() && !value.Equal(w, f.Value)
	case index.OpNotIn:
		if slices.ContainsFunc(f.Values, value.Value.IsNull) {
			return false
		}
		return !w.IsNull() && !slices.ContainsFunc(f.Values, func(x value.Value) bool { return value.Equal(w, x) })
	case index.OpLessThan:
		return value.InRange(value.Less, w, f.Value)
	case index.OpLessThanOrEqual:
		return value.InRange(value.LessOrEqual, w, f.Value)
	case index.OpGreaterThan:
		return value.InRange(value.Greater, w, f.Value)
	case index.OpGreaterThanOrEqual:
		return value.InRange(value.GreaterOrEqual, w, f.Value)
	case index.OpArrayContains:
		return contains(w, f.Value)
	case index.OpArrayContainsAny:
		return slices.ContainsFunc(f.Values, func(x value.Value) bool { return contains(w, x) })
	case index.OpIsNull:
		return w.IsNull()
	case index.OpIsNaN:
		return w.IsNaN()
	case index.OpIsNotNull:
		return !w.IsNull()
	case index.OpIsNotNaN:
		return !w.IsNull() && !w.IsNaN()
	}
	return false
}

// contains is array-contains of x in w.
func contains(w, x value.Value) bool {
	if x.IsNull() || x.IsNaN() {
		return slices.ContainsFunc(w.Elements(), func(e value.Value) bool {
			return e.Kind() == value.KindArray && len(e.Elements()) == 0
		})
	}
	return slices.ContainsFunc(w.Elements(), func(e value.Value) bool { return value.Equal(e, x) })
}
