package wire

import (
	"encoding/json"
	"math"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// recordedWrite is one write a values recorder sent: its fields (or one field v), production's
// verdict, and what production stored.
type recordedWrite struct {
	id      string
	fields  any // the document's fields, as JSON
	stored  any // the stored fields, nil if not recorded
	message string
	http    int
}

func valueWrites(t *testing.T) []recordedWrite {
	t.Helper()
	var out []recordedWrite
	type verdict struct {
		HTTP  int `json:"http"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	msg := func(v verdict) string {
		if v.Error == nil {
			return ""
		}
		return v.Error.Message
	}
	var validate struct {
		Cases []struct {
			ID     string  `json:"id"`
			Fields any     `json:"fields"`
			Stored any     `json:"stored"`
			Write  verdict `json:"write"`
		} `json:"cases"`
	}
	readCorpus(t, "values-validate.json", &validate)
	for _, c := range validate.Cases {
		out = append(out, recordedWrite{"values-validate/" + c.ID, c.Fields, c.Stored, msg(c.Write), c.Write.HTTP})
	}
	for _, file := range []string{"values-order.json", "values-bson.json"} {
		var corpus struct {
			Values []struct {
				ID     string  `json:"id"`
				Sent   any     `json:"sent"`
				Stored any     `json:"stored"`
				Write  verdict `json:"write"`
			} `json:"values"`
			Documents []struct {
				ID     string  `json:"id"`
				Fields any     `json:"fields"`
				Write  verdict `json:"write"`
			} `json:"documents"`
		}
		readCorpus(t, file, &corpus)
		for _, v := range corpus.Values {
			var stored any
			if v.Stored != nil {
				stored = map[string]any{"v": v.Stored}
			}
			out = append(out, recordedWrite{file + "/" + v.ID, map[string]any{"v": v.Sent}, stored, msg(v.Write), v.Write.HTTP})
		}
		for _, d := range corpus.Documents {
			out = append(out, recordedWrite{file + "/documents/" + d.ID, d.Fields, nil, msg(d.Write), d.Write.HTTP})
		}
	}
	return out
}

// knownDifferences are the recorded writes whose verdict this pipeline does not reproduce, and
// why; value's corpus test lists the same.
var knownDifferences = map[string]string{
	"values-bson.json/bad-min-max": "production names the first reserved key in request order ({__min__, __max__} as __min__); a Value keeps byte order",
}

// TestWritePipelineCorpus replays every value production was sent in a write
// (values-validate.json, values-order.json, values-bson.json) through what this package and
// value do with a REST commit: UnmarshalREST, FieldsFromProto, value.Validate. Production's
// verdict must come out, byte for byte, at whichever step finds it; a value production stored
// must come back as stored, after the write-time rules (TruncateMicros, CanonicalBSON).
func TestWritePipelineCorpus(t *testing.T) {
	writes := valueWrites(t)
	if len(writes) < 300 {
		t.Fatalf("only %d recorded writes", len(writes))
	}
	for _, w := range writes {
		fields := expandAll(t, w.fields)
		body := toJSON(t, map[string]any{"writes": []any{map[string]any{"update": map[string]any{
			"name": root + "/aruma-ref-values-bad/c000", "fields": fields}}}})
		got, v := writePipeline(body)
		if _, known := knownDifferences[w.id]; known {
			if got == w.message {
				t.Errorf("%s: matches production now; drop it from knownDifferences", w.id)
			}
			continue
		}
		if got != w.message {
			t.Errorf("%s: got %q, production %q", w.id, got, w.message)
			continue
		}
		if w.message != "" {
			if w.http != 400 {
				t.Errorf("%s: recorded HTTP %d", w.id, w.http)
			}
			continue
		}
		if w.stored == nil {
			continue
		}
		var st firestorepb.Document
		if err := UnmarshalREST(toJSON(t, map[string]any{"fields": expandAll(t, w.stored)}), &st); err != nil {
			t.Errorf("%s: stored: %v", w.id, err)
			continue
		}
		want, err := FieldsFromProto(st.GetFields())
		if err != nil {
			t.Errorf("%s: stored: %v", w.id, err)
			continue
		}
		if stored := storedForm(v); !value.Identical(stored, want) {
			t.Errorf("%s: stored as %v, production %v", w.id, stored, want)
		}
		// And the stored value goes back out as production's JSON.
		out, err := MarshalREST(&firestorepb.Document{Fields: FieldsToProto(want)})
		if err != nil {
			t.Errorf("%s: %v", w.id, err)
			continue
		}
		var back firestorepb.Document
		if err := UnmarshalREST(out, &back); err != nil {
			t.Errorf("%s: %s: %v", w.id, out, err)
			continue
		}
		if bv, err := FieldsFromProto(back.GetFields()); err != nil || !value.Identical(bv, want) {
			t.Errorf("%s: REST round trip %s: %v", w.id, out, err)
		}
	}
}

// writePipeline is the REST commit path up to the write: the first error's message, or the
// fields.
func writePipeline(body []byte) (string, value.Value) {
	var req firestorepb.CommitRequest
	if err := UnmarshalREST(body, &req); err != nil {
		return errText(err), value.Value{}
	}
	if _, err := Bind(&req, Database{corpusProject, DefaultDatabase}); err != nil {
		return errText(err), value.Value{}
	}
	_, fields, err := DocumentFromProto(req.GetWrites()[0].GetUpdate())
	if err != nil {
		return errText(err), value.Value{}
	}
	if err := value.Validate(fields); err != nil {
		return err.Error(), value.Value{}
	}
	return "", fields
}

// storedForm is what production stores for fields: timestamps cut to microseconds, BSON shapes
// canonical.
func storedForm(v value.Value) value.Value {
	switch v.Kind() {
	case value.KindTimestamp:
		return v.TruncateMicros()
	case value.KindArray:
		els := v.Elements()
		out := make([]value.Value, len(els))
		for i, e := range els {
			out[i] = storedForm(e)
		}
		return value.Array(out...)
	case value.KindMap:
		v = value.CanonicalBSON(v)
		fs := v.Fields()
		out := make([]value.Field, len(fs))
		for i, f := range fs {
			out[i] = value.Field{Name: f.Name, Value: storedForm(f.Value)}
		}
		return value.Map(out...)
	}
	return v
}

func TestConvertRoundTrip(t *testing.T) {
	dec, _ := value.Decimal128("1.5")
	vals := []value.Value{
		value.Null(), value.Bool(true), value.Bool(false), value.Int(math.MinInt64), value.Int(math.MaxInt64),
		value.Double(math.Copysign(0, -1)), value.Double(math.NaN()), value.Double(math.Inf(-1)), value.Double(5e-324),
		value.Timestamp(-62135596800, 0), value.Timestamp(253402300799, 999_999_999), value.Timestamp(0, 1),
		value.String(""), value.String("\x00é😀"), value.Bytes(nil), value.Bytes([]byte{0, 0xff}),
		value.Reference(root + "/c/__id-1__"), value.Reference("projects/other/databases/x-db1/documents/c/a/s/b"),
		value.GeoPoint(-90, 180), value.GeoPoint(math.Copysign(0, -1), 0),
		value.Array(), value.Array(value.Int(1), value.Array(value.Null())),
		value.Map(), value.Map(value.Field{Name: "", Value: value.Int(1)}, value.Field{Name: "a.b", Value: value.Map()}),
		value.Vector(1, -0.5), value.MinKey(), value.MaxKey(), value.Int32(-7), dec,
		value.BSONTimestampOf(1, 2), value.BSONBinaryOf(128, []byte{1}), value.ObjectID([12]byte{1, 2}), value.Regex("^a", "i"),
	}
	for _, v := range vals {
		pv := ValueToProto(v)
		b, err := proto.Marshal(pv)
		if err != nil {
			t.Fatalf("%v: %v", v, err)
		}
		var back firestorepb.Value
		if err := proto.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		got, err := ValueFromProto(&back)
		if err != nil || !value.Identical(got, v) {
			t.Errorf("%v: gRPC round trip %v, %v", v, got, err)
		}
		js, err := MarshalREST(pv)
		if err != nil {
			t.Fatalf("%v: %v", v, err)
		}
		var jback firestorepb.Value
		if err := UnmarshalREST(js, &jback); err != nil {
			t.Errorf("%v: %s: %v", v, js, err)
			continue
		}
		if got, err := ValueFromProto(&jback); err != nil || !value.Identical(got, v) {
			t.Errorf("%v: REST round trip via %s: %v, %v", v, js, got, err)
		}
	}
}

func TestValueFromProtoRefusals(t *testing.T) {
	for _, c := range []struct {
		v    *firestorepb.Value
		want string
		code Code
	}{
		{&firestorepb.Value{}, msgNoValue, CodeInvalidArgument},
		{nil, msgNoValue, CodeInvalidArgument},
		{&firestorepb.Value{ValueType: &firestorepb.Value_TimestampValue{TimestampValue: &timestamppb.Timestamp{Seconds: -62135596801}}},
			"Timestamp value exceeds limits", CodeInvalidArgument},
		{&firestorepb.Value{ValueType: &firestorepb.Value_TimestampValue{TimestampValue: &timestamppb.Timestamp{Nanos: 1e9}}},
			"Timestamp value exceeds limits", CodeInvalidArgument},
		{&firestorepb.Value{ValueType: &firestorepb.Value_ReferenceValue{ReferenceValue: "c/a"}},
			`Document parent name "c/a" lacks "projects" at index 0.`, CodeInvalidArgument},
		{&firestorepb.Value{ValueType: &firestorepb.Value_ArrayValue{ArrayValue: &firestorepb.ArrayValue{
			Values: []*firestorepb.Value{{ValueType: &firestorepb.Value_ReferenceValue{ReferenceValue: root + "/c/__x__"}}}}}},
			`Resource id "__x__" is invalid because it is reserved.`, CodeInvalidArgument},
		{&firestorepb.Value{ValueType: &firestorepb.Value_FieldReferenceValue{FieldReferenceValue: "a"}},
			"Pipeline expressions in values is not supported by this server", CodeUnimplemented},
	} {
		_, err := ValueFromProto(c.v)
		if e := From(err); err == nil || e.Message != c.want || e.Code != c.code {
			t.Errorf("%v: %v, want %v %q", c.v, err, c.code, c.want)
		}
	}
}

func TestDocumentProto(t *testing.T) {
	n, fields, err := DocumentFromProto(&firestorepb.Document{Name: root + "/c/a",
		Fields: map[string]*firestorepb.Value{"b": {ValueType: &firestorepb.Value_IntegerValue{IntegerValue: 2}},
			"a": {ValueType: &firestorepb.Value_NullValue{}}}})
	if err != nil || n.RelativePath() != "c/a" || len(fields.Fields()) != 2 || fields.Fields()[0].Name != "a" {
		t.Fatalf("%v %v %v", n, fields, err)
	}
	if _, _, err := DocumentFromProto(&firestorepb.Document{Name: root + "/c"}); err == nil {
		t.Error("a collection name was read as a document")
	}
	// A missing document listed with show_missing is its name alone (f0-list-show-missing.json).
	b, err := MarshalREST(DocumentToProto(root+"/aruma-ref-f0-tree/m", value.Map(), nil, nil))
	if err != nil || string(b) != `{"name":"`+root+`/aruma-ref-f0-tree/m"}` {
		t.Errorf("missing document: %s %v", b, err)
	}
	var raw map[string]any
	_ = json.Unmarshal(b, &raw)
	if _, ok := raw["fields"]; ok {
		t.Error("empty fields rendered")
	}
}

// TestCheckTimesF1 checks the read_time and precondition rules of f1-get.json and
// f1-update.json, with the recorded times' shapes (a commit time with 789 ns appended, the
// epoch).
func TestCheckTimesF1(t *testing.T) {
	const micros = "timestamp cannot have more than microseconds precision"
	for _, c := range []struct {
		ts         *timestamppb.Timestamp
		read, prec string
	}{
		{&timestamppb.Timestamp{Seconds: 1761502884, Nanos: 235_123_789}, micros, micros},
		{&timestamppb.Timestamp{Seconds: 1761502884, Nanos: 235_123_000}, "", ""},
		{&timestamppb.Timestamp{Seconds: 1761502884, Nanos: 1}, micros, micros},
		{&timestamppb.Timestamp{}, "Only timestamps past epoch are supported.", ""},
		{&timestamppb.Timestamp{Seconds: -1}, "Only timestamps past epoch are supported.", ""},
		{&timestamppb.Timestamp{Nanos: 1000}, "", ""},
	} {
		if got := errText(CheckReadTime(c.ts)); got != c.read {
			t.Errorf("CheckReadTime(%v) = %q, want %q", c.ts, got, c.read)
		}
		if got := errText(CheckMicros(c.ts)); got != c.prec {
			t.Errorf("CheckMicros(%v) = %q, want %q", c.ts, got, c.prec)
		}
	}
}
