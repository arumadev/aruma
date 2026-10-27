package query

import (
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// Cursor is a start_at or end_at: values for a prefix of the query's whole order, and whether
// the position is before the documents equal to them (query.proto, Cursor.before).
type Cursor struct {
	Values []value.Value
	Before bool
}

// cursorOf reads a cursor against the query's whole order. Production bounds its values by the
// explicit orders alone, not by the implicit ones that follow (f3-run-query.json
// cursor.with_name, cursor.name_only, cursor.inequality_implicit: "Cursor has too many
// values."), so a cursor holds one value per explicit order at most, and a reference for
// __name__.
func cursorOf(c *pb.Cursor, explicit, order []Order) (*Cursor, error) {
	if c == nil {
		return nil, nil
	}
	if len(c.GetValues()) > len(explicit) {
		return nil, errCursorValues
	}
	out := &Cursor{Before: c.GetBefore(), Values: make([]value.Value, len(c.GetValues()))}
	for i, pv := range c.GetValues() {
		v, err := wire.ValueFromProto(pv)
		if err != nil {
			return nil, err
		}
		if err := value.ValidateOperand(v); err != nil {
			return nil, wire.InvalidArgument(err.Error())
		}
		if order[i].Path.IsDocumentID() && v.Kind() != value.KindReference {
			return nil, errCursorName
		}
		out.Values[i] = v
	}
	return out, nil
}

// compareCursor compares a document's order values with a cursor's, on the cursor's prefix of
// the order, each in its order's direction: < 0 when the document comes before the cursor's
// position, > 0 after it, 0 when they tie on every value the cursor has.
func compareCursor(vals []value.Value, order []Order, c *Cursor) int {
	for i, cv := range c.Values {
		if r := order[i].compare(vals[i], cv); r != 0 {
			return r
		}
	}
	return 0
}

// afterStart reports whether a document is at or past the start cursor: a document tied with
// it is in when the cursor is before them (startAt), out when it is after (startAfter).
func afterStart(vals []value.Value, order []Order, c *Cursor) bool {
	if c == nil {
		return true
	}
	r := compareCursor(vals, order, c)
	return r > 0 || r == 0 && c.Before
}

// beforeEnd reports whether a document is before the end cursor: a tied document is in when
// the cursor is after them (endAt), out when it is before (endBefore).
func beforeEnd(vals []value.Value, order []Order, c *Cursor) bool {
	if c == nil {
		return true
	}
	r := compareCursor(vals, order, c)
	return r < 0 || r == 0 && !c.Before
}
