package api

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// orderClause is one clause of ListDocuments' order_by.
type orderClause struct {
	path value.FieldPath
	desc bool
}

// listOrder is a parsed order_by: the clauses as given, without the implicit __name__.
type listOrder []orderClause

// parseOrderBy reads ListDocuments' order_by as production does (f1-list-documents.json,
// order_by.*): clauses separated by commas, each a field path and an optional direction (asc or
// desc, in any case), spaces around them. Anything else is "Invalid order by clause" with the
// whole text; a field twice, or any field after __name__, has its own message. An empty
// order_by is no order.
func parseOrderBy(s string) (listOrder, error) {
	if s == "" {
		return nil, nil
	}
	invalid := wire.InvalidArgument(`Invalid order by clause "` + s + `".`)
	var out listOrder
	for clause := range strings.SplitSeq(s, ",") {
		words := strings.Fields(clause)
		if len(words) == 0 || len(words) > 2 {
			return nil, invalid
		}
		c := orderClause{}
		if len(words) == 2 {
			switch strings.ToLower(words[1]) {
			case "asc":
			case "desc":
				c.desc = true
			default:
				return nil, invalid
			}
		}
		p, err := value.ParseFieldPath(words[0])
		if err != nil {
			return nil, invalid
		}
		c.path = p
		if len(out) > 0 && out[len(out)-1].path.IsDocumentID() {
			return nil, wire.InvalidArgument("order by clause cannot contain more fields after the key")
		}
		for _, o := range out {
			if o.path.String() == p.String() {
				return nil, wire.InvalidArgument("order by clause cannot contain duplicate fields " + p.String())
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// byName reports whether the order is the default: none, or __name__ ascending alone.
func (o listOrder) byName() bool {
	return len(o) == 0 || len(o) == 1 && o[0].path.IsDocumentID() && !o[0].desc
}

// String is the order normalized, as a page token's context holds it.
func (o listOrder) String() string {
	if o.byName() {
		return ""
	}
	var b strings.Builder
	for i, c := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(c.path.String())
		if c.desc {
			b.WriteString(" desc")
		}
	}
	return b.String()
}

// comparePaths orders document paths as production orders names, by their keys (core's name
// keys): id by id, a numeric id __id<n>__ before every other and by n, other ids by their
// bytes, and a path before the paths below it.
func comparePaths(a, b core.Path) int { return slices.CompareFunc(a, b, compareIDs) }

func compareIDs(a, b string) int {
	na, aNumeric := numericID(a)
	nb, bNumeric := numericID(b)
	switch {
	case aNumeric && bNumeric:
		return cmp.Compare(na, nb)
	case aNumeric:
		return -1
	case bNumeric:
		return 1
	}
	return strings.Compare(a, b)
}

// numericID reads a numeric id, __id<n>__ with n a 64-bit integer written canonically.
func numericID(id string) (int64, bool) {
	digits, ok := strings.CutPrefix(id, "__id")
	if !ok {
		return 0, false
	}
	if digits, ok = strings.CutSuffix(digits, "__"); !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(digits, 10, 64)
	return n, err == nil && strconv.FormatInt(n, 10) == digits
}
