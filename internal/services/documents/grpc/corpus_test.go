package grpc_test

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	docgrpc "github.com/arumadev/aruma/internal/services/documents/grpc"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// grpcDiffers are the cases this server does not answer as production, with their parity
// exception (parity-exceptions.json). A listed case answered as production fails the test.
var grpcDiffers = map[string]string{
	"f3-query-limits.json ineq.11_fields":      "inequality-fields-order",
	"f3-query-limits.json grpc.ineq_11_fields": "inequality-fields-order",
}

// f3Files are the F3 recordings of the query methods.
var f3Files = []string{"f3-run-query.json", "f3-query-limits.json", "f3-aggregations.json", "f3-partition-query.json"}

type f3Case struct {
	ID          string   `json:"id"`
	Transport   string   `json:"transport"`
	Replay      bool     `json:"replay"`
	ElementKeys []string `json:"elementKeys"`
	Request     struct {
		Method  string          `json:"method"`
		Path    string          `json:"path"`
		Body    json.RawMessage `json:"body"`
		RPC     string          `json:"rpc"`
		Message json.RawMessage `json:"message"`
	} `json:"request"`
	Response struct {
		HTTP int             `json:"http"`
		Body json.RawMessage `json:"body"`
		OK   bool            `json:"ok"`
	} `json:"response"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type f3File struct {
	Setup struct {
		Commits []struct {
			Writes []struct {
				Update struct {
					Name   string          `json:"name"`
					Fields json.RawMessage `json:"fields"`
				} `json:"update"`
			} `json:"writes"`
		} `json:"commits"`
	} `json:"setup"`
	Cases []f3Case `json:"cases"`
}

// TestF3CorpusReplay replays the F3 recordings of RunQuery, RunAggregationQuery and
// PartitionQuery over gRPC, after their files' setup: every REST case marked replay, as the
// request message it carries, must get the answer production gave over REST, element for
// element (production answers both surfaces alike: f3-production-recordings.md, "gRPC"), but
// those the REST front end answers itself (a body it cannot read, a collection path); and every
// recorded gRPC case must get production's status and message, or its elements' keys and
// documents.
func TestF3CorpusReplay(t *testing.T) {
	x := newHarness(t, apitest.Options{}, docgrpc.Limits{})
	ctx, tok := t.Context(), x.env.JWT(t)
	var files []f3File
	for _, name := range f3Files {
		b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "oracle", "documents", "corpus", name))
		if err != nil {
			t.Fatal(err)
		}
		var f f3File
		if err := json.Unmarshal(b, &f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, c := range f.Setup.Commits {
			for _, w := range c.Writes {
				doc := &pb.Document{}
				if err := wire.UnmarshalREST([]byte(`{"fields":`+string(w.Update.Fields)+`}`), doc); err != nil {
					t.Fatal(err)
				}
				doc.Name = strings.ReplaceAll(w.Update.Name, "{project}", apitest.Project)
				if _, err := x.client.UpdateDocument(ctx, req(&pb.UpdateDocumentRequest{Document: doc}, tok)); err != nil {
					t.Fatalf("%s setup %s: %v", name, doc.Name, err)
				}
			}
		}
		files = append(files, f)
	}
	project := func(b []byte) []byte {
		return []byte(strings.ReplaceAll(string(b), "projects/{project}/", "projects/"+apitest.Project+"/"))
	}
	restSent, grpcSent := 0, 0
	listed := 0
	check := func(id, diff string) {
		if exception, ok := grpcDiffers[id]; ok {
			listed++
			if diff == "" {
				t.Errorf("%s: answered as production over gRPC, but listed as differing (%s)", id, exception)
			}
			return
		}
		if diff != "" {
			t.Errorf("%s over gRPC: %s", id, diff)
		}
	}
	for i, f := range files {
		for _, c := range f.Cases {
			id := f3Files[i] + " " + c.ID
			switch {
			case c.Transport == "rest" && c.Replay && !frontEndAnswer(c):
				parent, verb, _ := strings.Cut(string(project([]byte(c.Request.Path))), ":")
				body := c.Request.Body
				if string(body) == "null" || len(body) == 0 {
					body = []byte("{}")
				}
				got, err := x.send(t, verb, parent, project(body))
				restSent++
				check(id, restDiff(c, got, err))
			case c.Transport == "grpc" && c.Error != nil || c.Transport == "grpc" && c.ElementKeys != nil:
				var m struct {
					Parent string `json:"parent"`
				}
				if err := json.Unmarshal(c.Request.Message, &m); err != nil {
					t.Fatal(err)
				}
				verb := c.Request.RPC[strings.LastIndexByte(c.Request.RPC, '/')+1:]
				verb = strings.ToLower(verb[:1]) + verb[1:]
				// The recorder wrote a double that JSON cannot hold as "<number NaN>".
				msg := recordedNumber.ReplaceAll(project(c.Request.Message), []byte(`"$1"`))
				got, err := x.send(t, verb, string(project([]byte(m.Parent))), msg)
				grpcSent++
				check(id, grpcDiff(c, got, err))
			}
		}
	}
	t.Logf("%d REST cases and %d gRPC cases replayed over gRPC", restSent, grpcSent)
	if listed != len(grpcDiffers) {
		t.Errorf("%d of the %d cases listed as differing were sent", listed, len(grpcDiffers))
	}
	if restSent < 300 || grpcSent < 25 {
		t.Errorf("only %d REST and %d gRPC cases replayed", restSent, grpcSent)
	}
}

// recordedNumber is a recorded gRPC message's NaN or infinity.
var recordedNumber = regexp.MustCompile(`"<number ([-A-Za-z]+)>"`)

// frontEndAnswer reports a REST case whose answer comes from the front end, before Firestore
// reads the request: a body it cannot read into the message, or a collection path.
func frontEndAnswer(c f3Case) bool {
	var res struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	var arr []json.RawMessage
	b := c.Response.Body
	if json.Unmarshal(b, &arr) == nil && len(arr) > 0 {
		b = arr[0]
	}
	_ = json.Unmarshal(b, &res)
	return strings.HasPrefix(res.Error.Message, "Invalid JSON payload") || strings.HasPrefix(res.Error.Message, "Invalid value at")
}

// send calls the method verb with the REST body read as its request message, under parent, and
// returns each response as REST renders it.
func (x *harness) send(t *testing.T, verb, parent string, body []byte) ([]json.RawMessage, error) {
	t.Helper()
	ctx, tok := t.Context(), x.env.JWT(t)
	var out []json.RawMessage
	add := func(m proto.Message) {
		b, err := wire.MarshalREST(m)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	switch verb {
	case "runQuery":
		r := &pb.RunQueryRequest{}
		if err := wire.UnmarshalREST(body, r); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		r.Parent = parent
		s, err := x.client.RunQuery(ctx, req(r, tok))
		if err != nil {
			return nil, err
		}
		for s.Receive() {
			add(s.Msg())
		}
		return out, s.Err()
	case "runAggregationQuery":
		r := &pb.RunAggregationQueryRequest{}
		if err := wire.UnmarshalREST(body, r); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		r.Parent = parent
		s, err := x.client.RunAggregationQuery(ctx, req(r, tok))
		if err != nil {
			return nil, err
		}
		for s.Receive() {
			add(s.Msg())
		}
		return out, s.Err()
	case "partitionQuery":
		r := &pb.PartitionQueryRequest{}
		if err := wire.UnmarshalREST(body, r); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		r.Parent = parent
		res, err := x.client.PartitionQuery(ctx, req(r, tok))
		if err != nil {
			return nil, err
		}
		add(res.Msg)
		return out, nil
	}
	t.Fatalf("method %q", verb)
	return nil, nil
}

// restDiff compares gRPC's answer with production's REST answer: the error's status and message,
// or every element, its times and transaction normalised.
func restDiff(c f3Case, got []json.RawMessage, err error) string {
	var want []json.RawMessage
	if json.Unmarshal(c.Response.Body, &want) != nil {
		want = []json.RawMessage{c.Response.Body}
	}
	if c.Response.HTTP != 200 {
		var e struct {
			Error struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(want[0], &e) != nil {
			return "unreadable recording"
		}
		return statusDiff(err, e.Error.Status, e.Error.Message)
	}
	if err != nil {
		return "error " + err.Error()
	}
	if len(got) != len(want) {
		return "elements " + string(join(got)) + ", want " + string(join(want))
	}
	for i := range want {
		var g, w any
		if json.Unmarshal(normalise(got[i]), &g) != nil || json.Unmarshal(want[i], &w) != nil || !reflect.DeepEqual(g, w) {
			return "element " + string(normalise(got[i])) + ", want " + string(want[i])
		}
	}
	return ""
}

// grpcDiff compares an answer with a recorded gRPC case: its status and message, or the keys
// of each element and the documents' names in order.
func grpcDiff(c f3Case, got []json.RawMessage, err error) string {
	if c.Error != nil {
		return statusDiff(err, connect.Code(c.Error.Code).String(), c.Error.Message)
	}
	if err != nil {
		return "error " + err.Error()
	}
	if c.Request.RPC == "google.firestore.v1.Firestore/PartitionQuery" {
		return ""
	}
	var keys, names, wantNames []string
	for _, b := range got {
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) != nil {
			return "unreadable " + string(b)
		}
		keys = append(keys, strings.Join(slices.Sorted(maps.Keys(m)), ","))
		var d struct {
			Document struct {
				Name string `json:"name"`
			} `json:"document"`
		}
		_ = json.Unmarshal(b, &d)
		names = append(names, d.Document.Name)
	}
	var res struct {
		Responses []struct {
			Document struct {
				Name string `json:"name"`
			} `json:"document"`
		} `json:"responses"`
	}
	_ = json.Unmarshal(c.Response.Body, &res)
	for _, r := range res.Responses {
		wantNames = append(wantNames, r.Document.Name)
	}
	if !slices.Equal(keys, c.ElementKeys) {
		return "element keys " + strings.Join(keys, " | ") + ", want " + strings.Join(c.ElementKeys, " | ")
	}
	if res.Responses != nil && !slices.Equal(names, wantNames) {
		return "documents " + strings.Join(names, " ") + ", want " + strings.Join(wantNames, " ")
	}
	return ""
}

// statusDiff compares a gRPC error with a status, by its name in either case, and a message.
func statusDiff(err error, status, message string) string {
	ce, ok := errors.AsType[*connect.Error](err)
	if !ok {
		return "no gRPC status, want " + status + " " + message
	}
	if !strings.EqualFold(strings.ReplaceAll(ce.Code().String(), "_", ""), strings.ReplaceAll(status, "_", "")) || ce.Message() != message {
		return ce.Code().String() + " " + ce.Message() + ", want " + status + " " + message
	}
	return ""
}

// normalise replaces what a recording replaced: times, and a transaction's id.
func normalise(b []byte) []byte {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return b
	}
	var walk func(map[string]any)
	walk = func(m map[string]any) {
		for k, v := range m {
			switch k {
			case "readTime", "createTime", "updateTime":
				m[k] = "<" + k + ": RFC 3339>"
			case "transaction":
				m[k] = "<transaction>"
			case "document":
				if d, ok := v.(map[string]any); ok {
					walk(d)
				}
			}
		}
	}
	walk(m)
	out, _ := json.Marshal(m)
	return out
}

func join(bs []json.RawMessage) []byte {
	out, _ := json.Marshal(bs)
	return out
}
