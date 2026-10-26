package documents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"slices"
	"strings"
	"testing"

	"cloud.google.com/go/firestore"
	vkit "cloud.google.com/go/firestore/apiv1"
	pb "cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// recordedProject is the production project of the recordings; their references and messages
// name it, and the tests put this server's project in its place.
const recordedProject = "chatapp-8ae21"

// clientOptions point a client at Aruma the way a developer would: the endpoint, gRPC without
// TLS, and, with creds, the key file Aruma issued.
func clientOptions(creds bool) []option.ClientOption {
	opts := []option.ClientOption{
		option.WithEndpoint(live.addr),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		option.WithGRPCDialOption(grpc.WithContextDialer(func(ctx context.Context, a string) (net.Conn, error) {
			return guardDial(ctx, "tcp", a)
		})),
	}
	if creds {
		return append(opts, option.WithAuthCredentialsFile(option.ServiceAccount, keyFile))
	}
	return append(opts, option.WithoutAuthentication())
}

func newClient(t T) *firestore.Client {
	t.Helper()
	c, err := firestore.NewClient(context.Background(), live.expected.ProjectID, clientOptions(true)...)
	if err != nil {
		t.Fatalf("firestore.NewClient: %v", err)
	}
	return c
}

func newRaw(t T, creds bool) *vkit.Client {
	t.Helper()
	c, err := vkit.NewClient(context.Background(), clientOptions(creds)...)
	if err != nil {
		t.Fatalf("apiv1.NewClient: %v", err)
	}
	return c
}

func database() string { return "projects/" + live.expected.ProjectID + "/databases/(default)" }
func root() string     { return database() + "/documents" }

// local rewrites the recorded project's name into this server's, in a reference or a message.
func local(s string) string {
	return strings.ReplaceAll(s, "projects/"+recordedProject+"/", "projects/"+live.expected.ProjectID+"/")
}

// wantCode checks err's gRPC code and, when msg is not empty, its message exactly.
func wantCode(t T, err error, code codes.Code, msg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %v", code)
	}
	st := status.Convert(err)
	if st.Code() != code {
		t.Fatalf("got %v, want code %v", err, code)
	}
	if msg != "" && st.Message() != msg {
		t.Errorf("code %v as expected, message\n got: %s\nwant: %s", code, st.Message(), msg)
	}
}

func mustOK(t T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// corpusValue is one value of values-order.json.
type corpusValue struct {
	ID     string
	Sent   *pb.Value
	Stored *pb.Value // nil when production refused the write
	Error  struct {
		Message, Status string
	}
}

func loadValues(t T) []corpusValue {
	t.Helper()
	b, err := os.ReadFile("../../../oracle/documents/corpus/values-order.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Values []struct {
			ID     string          `json:"id"`
			Sent   json.RawMessage `json:"sent"`
			Stored json.RawMessage `json:"stored"`
			Write  struct {
				HTTP  int `json:"http"`
				Error struct {
					Message string `json:"message"`
					Status  string `json:"status"`
				} `json:"error"`
			} `json:"write"`
		} `json:"values"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}
	parse := func(id string, raw json.RawMessage) *pb.Value {
		v := new(pb.Value)
		if err := protojson.Unmarshal([]byte(local(string(raw))), v); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		return v
	}
	var out []corpusValue
	for _, v := range file.Values {
		c := corpusValue{ID: v.ID, Sent: parse(v.ID, v.Sent)}
		if v.Write.HTTP == 200 {
			c.Stored = parse(v.ID, v.Stored)
		} else {
			c.Error.Message, c.Error.Status = local(v.Write.Error.Message), v.Write.Error.Status
		}
		out = append(out, c)
	}
	if len(out) < 200 {
		t.Fatalf("values-order.json holds %d values", len(out))
	}
	return out
}

// sameValue compares two values exactly: doubles bit for bit (so -0 is not 0) except that every
// NaN equals every NaN, maps by key, arrays in order.
func sameValue(a, b *pb.Value) bool {
	switch x := a.GetValueType().(type) {
	case *pb.Value_NullValue:
		_, ok := b.GetValueType().(*pb.Value_NullValue)
		return ok
	case *pb.Value_BooleanValue:
		y, ok := b.GetValueType().(*pb.Value_BooleanValue)
		return ok && x.BooleanValue == y.BooleanValue
	case *pb.Value_IntegerValue:
		y, ok := b.GetValueType().(*pb.Value_IntegerValue)
		return ok && x.IntegerValue == y.IntegerValue
	case *pb.Value_DoubleValue:
		y, ok := b.GetValueType().(*pb.Value_DoubleValue)
		return ok && sameFloat(x.DoubleValue, y.DoubleValue)
	case *pb.Value_TimestampValue:
		y, ok := b.GetValueType().(*pb.Value_TimestampValue)
		return ok && proto.Equal(x.TimestampValue, y.TimestampValue)
	case *pb.Value_StringValue:
		y, ok := b.GetValueType().(*pb.Value_StringValue)
		return ok && x.StringValue == y.StringValue
	case *pb.Value_BytesValue:
		y, ok := b.GetValueType().(*pb.Value_BytesValue)
		return ok && bytes.Equal(x.BytesValue, y.BytesValue)
	case *pb.Value_ReferenceValue:
		y, ok := b.GetValueType().(*pb.Value_ReferenceValue)
		return ok && x.ReferenceValue == y.ReferenceValue
	case *pb.Value_GeoPointValue:
		y, ok := b.GetValueType().(*pb.Value_GeoPointValue)
		return ok && sameFloat(x.GeoPointValue.GetLatitude(), y.GeoPointValue.GetLatitude()) &&
			sameFloat(x.GeoPointValue.GetLongitude(), y.GeoPointValue.GetLongitude())
	case *pb.Value_ArrayValue:
		y, ok := b.GetValueType().(*pb.Value_ArrayValue)
		return ok && slices.EqualFunc(x.ArrayValue.GetValues(), y.ArrayValue.GetValues(), sameValue)
	case *pb.Value_MapValue:
		y, ok := b.GetValueType().(*pb.Value_MapValue)
		return ok && sameFields(x.MapValue.GetFields(), y.MapValue.GetFields())
	}
	return false
}

func sameFields(a, b map[string]*pb.Value) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || !sameValue(v, w) {
			return false
		}
	}
	return true
}

func sameFloat(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return math.Float64bits(a) == math.Float64bits(b)
}

func show(m proto.Message) string {
	s := protojson.Format(m)
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return s
}

// batchGet runs BatchGetDocuments and returns its responses in order.
func batchGet(ctx context.Context, c *vkit.Client, req *pb.BatchGetDocumentsRequest) ([]*pb.BatchGetDocumentsResponse, error) {
	stream, err := c.BatchGetDocuments(ctx, req)
	if err != nil {
		return nil, err
	}
	var out []*pb.BatchGetDocumentsResponse
	for {
		r, err := stream.Recv()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
}

func fields(kv ...any) map[string]*pb.Value {
	m := map[string]*pb.Value{}
	for i := 0; i < len(kv); i += 2 {
		m[kv[i].(string)] = pv(kv[i+1])
	}
	return m
}

// pv makes a protobuf value of a few Go types.
func pv(x any) *pb.Value {
	switch x := x.(type) {
	case nil:
		return &pb.Value{ValueType: &pb.Value_NullValue{}}
	case int:
		return &pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: int64(x)}}
	case string:
		return &pb.Value{ValueType: &pb.Value_StringValue{StringValue: x}}
	case bool:
		return &pb.Value{ValueType: &pb.Value_BooleanValue{BooleanValue: x}}
	case map[string]*pb.Value:
		return &pb.Value{ValueType: &pb.Value_MapValue{MapValue: &pb.MapValue{Fields: x}}}
	}
	panic(fmt.Sprintf("pv %T", x))
}

func exists(b bool) *pb.Precondition {
	return &pb.Precondition{ConditionType: &pb.Precondition_Exists{Exists: b}}
}

// ensure keeps t's helpers honest: *testing.T is a T.
var _ T = (*testing.T)(nil)
