package api

import (
	"crypto/rand"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// maskPaths parses a DocumentMask's paths, read or update mask alike, with production's
// messages (values-mask.json, values-mask-vector.json; f1-get.json mask_empty_path). A nil mask
// is no mask: ok is false.
func maskPaths(m *firestorepb.DocumentMask) (paths []value.FieldPath, ok bool, err error) {
	if m == nil {
		return nil, false, nil
	}
	paths = make([]value.FieldPath, 0, len(m.GetFieldPaths()))
	for _, s := range m.GetFieldPaths() {
		p, err := value.ParseFieldPath(s)
		if err != nil {
			return nil, false, wire.InvalidArgument(err.Error())
		}
		if err := value.ValidateMaskPath(p); err != nil {
			return nil, false, wire.InvalidArgument(err.Error())
		}
		paths = append(paths, p)
	}
	return paths, true, nil
}

// readOptions reads a request's consistency selector: a read_time, checked as production checks
// it before its window (wire.CheckReadTime; the window is core's), or a transaction, whose id core
// checks (ADR-025 §7.5). The oneof holds one of them: the transports refuse both before this.
func readOptions(readTime *timestamppb.Timestamp, txn *core.TransactionRef) (core.ReadOptions, error) {
	if txn != nil {
		return core.ReadOptions{Transaction: txn}, nil
	}
	if readTime == nil {
		return core.ReadOptions{}, nil
	}
	if err := wire.CheckReadTime(readTime); err != nil {
		return core.ReadOptions{}, err
	}
	return core.ReadOptions{ReadTime: readTime.AsTime()}, nil
}

// documentProto is a stored document as a read returns it, through the read mask when there is
// one. A document that does not exist (show_missing) is its name alone.
func documentProto(db core.DB, d core.Document, mask []value.FieldPath, masked bool) *firestorepb.Document {
	name := db.DocumentName(d.Path)
	if !d.Exists() {
		return &firestorepb.Document{Name: name}
	}
	fields := d.Fields
	if masked {
		fields = value.Project(fields, mask)
	}
	return wire.DocumentToProto(name, fields, timestamp(d.CreateTime), timestamp(d.UpdateTime))
}

func timestamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

// stored is a write's fields as production stores them: timestamps cut to microseconds and BSON
// shapes in canonical form (values-order.json t-nanos, values-bson.json).
func stored(v value.Value) value.Value {
	return truncateTimes(value.CanonicalBSON(v))
}

func truncateTimes(v value.Value) value.Value {
	switch v.Kind() {
	case value.KindTimestamp:
		return v.TruncateMicros()
	case value.KindArray:
		els := v.Elements()
		out := make([]value.Value, len(els))
		for i, e := range els {
			out[i] = truncateTimes(e)
		}
		return value.Array(out...)
	case value.KindMap:
		fs := v.Fields()
		out := make([]value.Field, len(fs))
		for i, f := range fs {
			out[i] = value.Field{Name: f.Name, Value: truncateTimes(f.Value)}
		}
		return value.Map(out...)
	}
	return v
}

// fieldsOf converts a written document's fields, checked as production checks a write without
// an update mask (value.Validate).
func fieldsOf(d *firestorepb.Document) (value.Value, error) {
	fields, err := wire.FieldsFromProto(d.GetFields())
	if err != nil {
		return value.Value{}, err
	}
	if err := value.Validate(fields); err != nil {
		return value.Value{}, wire.InvalidArgument(err.Error())
	}
	return stored(fields), nil
}

// precondition reads a current_document. Its update_time must be whole microseconds
// (f1-update.json update_time_plus_1ns).
func precondition(p *firestorepb.Precondition) (core.Precondition, error) {
	switch c := p.GetConditionType().(type) {
	case *firestorepb.Precondition_Exists:
		return core.Precondition{Exists: new(c.Exists)}, nil
	case *firestorepb.Precondition_UpdateTime:
		if err := wire.CheckMicros(c.UpdateTime); err != nil {
			return core.Precondition{}, err
		}
		return core.Precondition{UpdateTime: c.UpdateTime.AsTime()}, nil
	}
	return core.Precondition{}, nil
}

// autoIDChars are the characters of an id CreateDocument chooses, as the client libraries choose
// theirs: 20 of 62 letters and digits.
const autoIDChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// autoID returns a random document id.
func autoID() string {
	b := make([]byte, 20)
	for i := range b {
		// rejection sampling keeps the 62 characters equally likely
		for {
			var r [1]byte
			_, _ = rand.Read(r[:])
			if int(r[0]) < 248 {
				b[i] = autoIDChars[int(r[0])%len(autoIDChars)]
				break
			}
		}
	}
	return string(b)
}

// pathOf is a parsed name's path in core's form.
func pathOf(n wire.Name) core.Path { return core.Path(n.Path) }
