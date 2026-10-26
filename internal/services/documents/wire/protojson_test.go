package wire

import (
	"encoding/json"
	"math"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// restMethods maps a REST custom method to its request message.
var restMethods = map[string]func() proto.Message{
	"commit":              func() proto.Message { return &firestorepb.CommitRequest{} },
	"batchGet":            func() proto.Message { return &firestorepb.BatchGetDocumentsRequest{} },
	"runQuery":            func() proto.Message { return &firestorepb.RunQueryRequest{} },
	"runAggregationQuery": func() proto.Message { return &firestorepb.RunAggregationQueryRequest{} },
	"beginTransaction":    func() proto.Message { return &firestorepb.BeginTransactionRequest{} },
	"rollback":            func() proto.Message { return &firestorepb.RollbackRequest{} },
	"listCollectionIds":   func() proto.Message { return &firestorepb.ListCollectionIdsRequest{} },
	"partitionQuery":      func() proto.Message { return &firestorepb.PartitionQueryRequest{} },
	"batchWrite":          func() proto.Message { return &firestorepb.BatchWriteRequest{} },
}

// placeholders stand in for the recorded times and transaction ids: a plausible value of each.
func fillPlaceholders(key string, x any) (any, bool) {
	switch y := x.(type) {
	case string:
		if !strings.HasPrefix(y, "<") || compactRE.MatchString(y) {
			return y, true
		}
		switch {
		case key == "transaction":
			return "dHJhbnNhY3Rpb24=", true
		case strings.HasSuffix(key, "Time") || key == "readTime":
			return "2026-10-26T18:21:24.235Z", true
		}
		return nil, false
	case map[string]any:
		out := make(map[string]any, len(y))
		for k, v := range y {
			f, ok := fillPlaceholders(k, v)
			if !ok {
				return nil, false
			}
			out[k] = f
		}
		return out, true
	case []any:
		out := make([]any, len(y))
		for i, v := range y {
			f, ok := fillPlaceholders(key, v)
			if !ok {
				return nil, false
			}
			out[i] = f
		}
		return out, true
	}
	return x, true
}

// recordedBodies walks a corpus file for REST requests with a body, by custom method.
func recordedBodies(t *testing.T, x any, visit func(method string, body any)) {
	switch y := x.(type) {
	case map[string]any:
		if p, ok := y["path"].(string); ok && y["method"] == "POST" {
			if body, ok := y["body"]; ok {
				if i := strings.LastIndex(p, ":"); i >= 0 {
					visit(p[i+1:], body)
				}
			}
		}
		for _, v := range y {
			recordedBodies(t, v, visit)
		}
	case []any:
		for _, v := range y {
			recordedBodies(t, v, visit)
		}
	}
}

// TestRESTBodiesMatchProtojson reads every REST request body the recorders sent as UnmarshalREST
// and as protojson do: where production accepted the body (every valid one), both must give the
// same message.
func TestRESTBodiesMatchProtojson(t *testing.T) {
	files := []string{"f0-errors.json", "f0-missing-database.json", "f0-explain.json", "f0-find-nearest.json",
		"f0-descendants.json", "f0-list-show-missing.json", "f0-contention.json", "indexes-105.json",
		"f1-batch-get.json", "f1-list-collection-ids.json", "f1-resource-names.json", "f1-write-results.json"}
	n := 0
	for _, file := range files {
		var corpus any
		readCorpus(t, file, &corpus)
		recordedBodies(t, corpus, func(method string, body any) {
			newMsg, ok := restMethods[method]
			if !ok {
				return
			}
			filled, ok := fillPlaceholders("", expandAll(t, body))
			if !ok {
				return
			}
			raw := []byte(strings.ReplaceAll(string(toJSON(t, filled)), "{project}", corpusProject))
			raw = []byte(strings.ReplaceAll(string(raw), "{root}", root))
			want := newMsg()
			if err := protojson.Unmarshal(raw, want); err != nil {
				return // a body production refused; TestFrontEndErrors and the value corpus judge those
			}
			got := newMsg()
			if err := UnmarshalREST(raw, got); err != nil {
				t.Errorf("%s %s: %v\n%s", file, method, err, raw[:min(len(raw), 300)])
				return
			}
			if !proto.Equal(got, want) {
				t.Errorf("%s %s: decoded differently\n got  %v\n want %v", file, method, got, want)
			}
			n++
		})
	}
	if n < 300 {
		t.Errorf("only %d bodies compared", n)
	}
}

// TestFrontEndErrorsF1 replays the f1 requests the front end refused while reading them: a
// oneof set twice in a body and in query parameters, and query values that do not parse.
func TestFrontEndErrorsF1(t *testing.T) {
	type entry struct {
		file, id string
		msg      func() proto.Message
		pathVars []string
	}
	cases := []entry{
		{"f1-batch-get.json", "rest.txn.and_new_transaction", func() proto.Message { return &firestorepb.BatchGetDocumentsRequest{} }, []string{"database"}},
		{"f1-batch-get.json", "rest.txn.and_read_time", func() proto.Message { return &firestorepb.BatchGetDocumentsRequest{} }, []string{"database"}},
		{"f1-get.json", "rest.txn.and_read_time", func() proto.Message { return &firestorepb.GetDocumentRequest{} }, nil},
		{"f1-get.json", "rest.txn.not_base64", func() proto.Message { return &firestorepb.GetDocumentRequest{} }, nil},
		{"f1-list-documents.json", "rest.page_size_not_number", func() proto.Message { return &firestorepb.ListDocumentsRequest{} }, nil},
		{"f1-update.json", "rest.exists_and_update_time", func() proto.Message { return &firestorepb.UpdateDocumentRequest{} }, nil},
	}
	for _, c := range cases {
		var corpus struct{ Cases []f0Case }
		readCorpus(t, c.file, &corpus)
		found := false
		for _, rc := range corpus.Cases {
			if rc.ID != c.id {
				continue
			}
			found = true
			m := c.msg()
			var err error
			if rc.Request.Method == "GET" || rc.Request.Method == "PATCH" {
				_, query, _ := strings.Cut(rc.Request.Path, "?")
				query = fillQuery(query)
				q, perr := url.ParseQuery(query)
				if perr != nil {
					t.Fatalf("%s: %v", c.id, perr)
				}
				err = UnmarshalQuery(q, m)
			} else {
				filled, _ := fillPlaceholders("", rc.Request.Body)
				// The recorder sent the members in this order; its corpus sorts them.
				raw := orderedBody(t, filled, "documents", "transaction", "newTransaction", "readTime")
				err = UnmarshalREST(raw, m, c.pathVars...)
			}
			if got := errText(err); got != rc.Error.Message {
				t.Errorf("%s %s:\n got  %q\n want %q", c.file, c.id, got, rc.Error.Message)
			}
		}
		if !found {
			t.Errorf("%s %s not recorded", c.file, c.id)
		}
	}
}

// fillQuery replaces the recorded placeholders in a query string by plausible values.
func fillQuery(q string) string {
	for {
		i := strings.Index(q, "<")
		if i < 0 {
			return q
		}
		j := strings.Index(q[i:], ">")
		if j < 0 {
			return q
		}
		v := url.QueryEscape("2026-10-26T18:21:24.235Z")
		if strings.HasPrefix(q[i:], "<the read-only transaction") {
			v = "dHJhbnNhY3Rpb24%3D"
		}
		q = q[:i] + v + q[i+j+1:]
	}
}

// orderedBody encodes a JSON object with its members in the given order first.
func orderedBody(t *testing.T, x any, order ...string) []byte {
	m := x.(map[string]any)
	var b strings.Builder
	b.WriteByte('{')
	first := true
	write := func(k string) {
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.Write(toJSON(t, k))
		b.WriteByte(':')
		b.Write(toJSON(t, m[k]))
		delete(m, k)
	}
	for _, k := range order {
		if _, ok := m[k]; ok {
			write(k)
		}
	}
	for k := range m {
		write(k)
	}
	b.WriteByte('}')
	return []byte(b.String())
}

func TestFrontEndErrors(t *testing.T) {
	const w0 = "writes[0].update.fields[0].value"
	commit := func(fields string) string {
		return `{"writes":[{"update":{"name":"` + root + `/c/a","fields":` + fields + `}}]}`
	}
	for _, c := range []struct{ body, want string }{
		// Recorded (values-validate.json).
		{commit(`{"d":{"integerValue":"9223372036854775808"}}`), "Invalid value at '" + w0 + `.integer_value' (TYPE_INT64), "9223372036854775808"`},
		{commit(`{"d":{"bytesValue":"***"}}`), "Invalid value at '" + w0 + `.bytes_value' (TYPE_BYTES), Base64 decoding failed for "***"`},
		{commit(`{"d":{"timestampValue":"0000-12-31T23:59:59Z"}}`), "Invalid value at '" + w0 + ".timestamp_value' (type.googleapis.com/google.protobuf.Timestamp), Field 'timestampValue', Timestamp value exceeds limits"},
		{commit(`{"d":{"timestampValue":"10000-01-01T00:00:00Z"}}`), "Invalid value at '" + w0 + ".timestamp_value' (type.googleapis.com/google.protobuf.Timestamp), Field 'timestampValue', Timestamp value exceeds limits"},
		// A map entry is addressed by its position in the body.
		{commit(`{"z":{"integerValue":"1"},"a":{"integerValue":"x"}}`), "Invalid value at 'writes[0].update.fields[1].value.integer_value' (TYPE_INT64), \"x\""},
		{commit(`{"a":{"arrayValue":{"values":[{"nullValue":null},{"integerValue":1.5}]}}}`), "Invalid value at '" + w0 + ".array_value.values[1].integer_value' (TYPE_INT64), 1.5"},
		// Provisional: the front end's forms as known.
		{`{"writes":[],"bogus":1}`, `Invalid JSON payload received. Unknown name "bogus": Cannot find field.`},
		{commit(`{"a":{"stringValue":"x","integerValue":"1"}}`), "Invalid value at '" + w0 + "' (oneof), oneof field 'value_type' is already set. Cannot set 'integerValue'"},
		{`{"writes":{"update":{}}}`, "Invalid value at 'writes' (type.googleapis.com/google.firestore.v1.Write), {\"update\":{}}"},
		{`{"database":["x"]}`, `Invalid JSON payload received. Unknown name "database": Proto field is not repeating, cannot start list.`},
		{`{"database":{"x":1}}`, `Invalid JSON payload received. Unknown name "database": Starting an object on a scalar field`},
		{`{"database":5}`, "Invalid value at 'database' (TYPE_STRING), 5"},
		{`{"writes":[{"update":5}]}`, "Invalid value at 'writes[0].update' (type.googleapis.com/google.firestore.v1.Document), 5"},
		{`{"a":1,"b":2}`, "Invalid JSON payload received. Unknown name \"a\": Cannot find field.\nInvalid JSON payload received. Unknown name \"b\": Cannot find field."},
		{`{"writes":[{"currentDocument":{"exists":"maybe"}}]}`, `Invalid value at 'writes[0].current_document.exists' (TYPE_BOOL), "maybe"`},
		{`[]`, "Invalid JSON payload received. Root element must be a message."},
		{`{"writes":`, "Invalid JSON payload received. Unexpected token."},
		{`{} {}`, "Invalid JSON payload received. Unexpected token."},
	} {
		var req firestorepb.CommitRequest
		if got := errText(UnmarshalREST([]byte(c.body), &req)); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.body, got, c.want)
		}
	}
	var q firestorepb.RunQueryRequest
	err := UnmarshalREST([]byte(`{"structuredQuery":{"where":{"fieldFilter":{"op":"BOGUS"}}}}`), &q)
	if want := `Invalid value at 'structured_query.where.field_filter.op' (type.googleapis.com/google.firestore.v1.StructuredQuery.FieldFilter.Operator), "BOGUS"`; errText(err) != want {
		t.Errorf("enum: %q", errText(err))
	}
}

func TestUnmarshalRESTForms(t *testing.T) {
	var q firestorepb.RunQueryRequest
	body := `{"parent":"` + root + `","structured_query":{"from":[{"collectionId":"c","allDescendants":true}],
		"where":{"fieldFilter":{"field":{"fieldPath":"a"},"op":3,"value":{"doubleValue":"-Infinity"}}},
		"limit":5,"offset":"2","startAt":{"values":[{"bytesValue":"-_8"}],"before":true}},
		"readTime":"2026-01-01T01:00:00.5+01:00"}`
	if err := UnmarshalREST([]byte(body), &q); err != nil {
		t.Fatal(err)
	}
	sq := q.GetStructuredQuery()
	if q.GetParent() != root || !sq.GetFrom()[0].GetAllDescendants() || sq.GetLimit().GetValue() != 5 || sq.GetOffset() != 2 ||
		sq.GetWhere().GetFieldFilter().GetOp() != firestorepb.StructuredQuery_FieldFilter_GREATER_THAN ||
		!math.IsInf(sq.GetWhere().GetFieldFilter().GetValue().GetDoubleValue(), -1) ||
		string(sq.GetStartAt().GetValues()[0].GetBytesValue()) != "\xfb\xff" {
		t.Errorf("decoded %v", &q)
	}
	if rt := q.GetReadTime(); rt.GetSeconds() != 1767225600 || rt.GetNanos() != 500_000_000 {
		t.Errorf("read time %v", rt)
	}
	// null is absence, except Firestore's nullValue; an empty body is an empty message.
	var c firestorepb.CommitRequest
	if err := UnmarshalREST([]byte(`{"transaction":null,"writes":[{"update":{"fields":{"n":{"nullValue":null}}}}]}`), &c); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.GetWrites()[0].GetUpdate().GetFields()["n"].GetValueType().(*firestorepb.Value_NullValue); !ok || c.Transaction != nil {
		t.Errorf("nulls: %v", &c)
	}
	if err := UnmarshalREST([]byte("  "), &c); err != nil {
		t.Error(err)
	}
	// Well-known types other than Timestamp, through protojson.
	var f adminpb.UpdateFieldRequest
	if err := UnmarshalREST([]byte(`{"field":{"name":"x"},"updateMask":"indexConfig,ttlConfig"}`), &f); err != nil ||
		strings.Join(f.GetUpdateMask().GetPaths(), ",") != "index_config,ttl_config" {
		t.Errorf("field mask: %v %v", f.GetUpdateMask(), err)
	}
	if err := UnmarshalREST([]byte(`{"updateMask":7}`), &f); errText(err) != "Invalid value at 'update_mask' (type.googleapis.com/google.protobuf.FieldMask), 7" {
		t.Errorf("bad field mask: %v", err)
	}
}

func TestUnmarshalQuery(t *testing.T) {
	var g firestorepb.GetDocumentRequest
	q := url.Values{"mask.fieldPaths": {"a", "`b c`"}, "transaction": {"dHhu"}, "key": {"AIza"}, "alt": {"json"}}
	if err := UnmarshalQuery(q, &g); err != nil {
		t.Fatal(err)
	}
	if strings.Join(g.GetMask().GetFieldPaths(), "|") != "a|`b c`" || string(g.GetTransaction()) != "txn" {
		t.Errorf("%v", &g)
	}
	var l firestorepb.ListDocumentsRequest
	if err := UnmarshalQuery(url.Values{"pageSize": {"2"}, "showMissing": {"true"}, "read_time": {"2026-01-01T00:00:00Z"}}, &l); err != nil ||
		l.GetPageSize() != 2 || !l.GetShowMissing() || l.GetReadTime().GetSeconds() != 1767225600 {
		t.Errorf("%v %v", &l, err)
	}
	err := UnmarshalQuery(url.Values{"bogus": {"1"}, "mask.nope": {"x"}}, &g)
	want := "Invalid JSON payload received. Unknown name \"bogus\": Cannot bind query parameter. Field 'bogus' could not be found in request message.\n" +
		"Invalid JSON payload received. Unknown name \"mask.nope\": Cannot bind query parameter. Field 'nope' could not be found in request message."
	if errText(err) != want {
		t.Errorf("unknown parameters: %q", errText(err))
	}
}

func TestMarshalREST(t *testing.T) {
	ts := func(s int64, n int32) *firestorepb.Value {
		return &firestorepb.Value{ValueType: &firestorepb.Value_TimestampValue{TimestampValue: &timestamppb.Timestamp{Seconds: s, Nanos: n}}}
	}
	for _, c := range []struct {
		v    *firestorepb.Value
		want string
	}{
		// 0, 3, 6 or 9 fractional digits (values-validate.json time.offset, time.max_nanos).
		{ts(1767225600, 500_000_000), `{"timestampValue":"2026-01-01T00:00:00.500Z"}`},
		{ts(253402300799, 999_999_000), `{"timestampValue":"9999-12-31T23:59:59.999999Z"}`},
		{ts(0, 0), `{"timestampValue":"1970-01-01T00:00:00Z"}`},
		{ts(0, 1), `{"timestampValue":"1970-01-01T00:00:00.000000001Z"}`},
		{&firestorepb.Value{ValueType: &firestorepb.Value_IntegerValue{IntegerValue: -9223372036854775808}}, `{"integerValue":"-9223372036854775808"}`},
		{&firestorepb.Value{ValueType: &firestorepb.Value_DoubleValue{DoubleValue: math.NaN()}}, `{"doubleValue":"NaN"}`},
		{&firestorepb.Value{ValueType: &firestorepb.Value_DoubleValue{DoubleValue: math.Inf(-1)}}, `{"doubleValue":"-Infinity"}`},
		{&firestorepb.Value{ValueType: &firestorepb.Value_DoubleValue{DoubleValue: 1e300}}, `{"doubleValue":1e+300}`},
		{&firestorepb.Value{ValueType: &firestorepb.Value_BytesValue{BytesValue: []byte{0xfb, 0xff}}}, `{"bytesValue":"+/8="}`},
		{&firestorepb.Value{ValueType: &firestorepb.Value_NullValue{}}, `{"nullValue":null}`},
	} {
		b, err := MarshalREST(c.v)
		if err != nil || string(b) != c.want {
			t.Errorf("%v: %s, want %s (%v)", c.v, b, c.want, err)
		}
	}
	// PartitionQuery with no partitions is {} (f0-descendants.json).
	if b, _ := MarshalREST(&firestorepb.PartitionQueryResponse{}); string(b) != "{}" {
		t.Errorf("empty: %s", b)
	}
	b, err := MarshalRESTStream([]*firestorepb.RunQueryResponse{
		{ReadTime: &timestamppb.Timestamp{Seconds: 1}},
		{SkippedResults: 2},
	})
	var arr []map[string]any
	if err != nil || json.Unmarshal(b, &arr) != nil || len(arr) != 2 || arr[0]["readTime"] != "1970-01-01T00:00:01Z" || arr[1]["skippedResults"] != float64(2) {
		t.Errorf("stream: %s %v", b, err)
	}
	if b, _ := MarshalRESTStream([]*firestorepb.RunQueryResponse{}); string(b) != "[]" {
		t.Errorf("empty stream: %s", b)
	}
}
