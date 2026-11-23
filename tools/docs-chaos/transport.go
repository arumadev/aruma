package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb/firestorepbconnect"
)

// api is the Document Database as a client sees it, over gRPC or over REST: the same requests,
// so a client can pick either for each call.
type api interface {
	Name() string
	Commit(ctx context.Context, r *pb.CommitRequest) (*pb.CommitResponse, error)
	BatchWrite(ctx context.Context, r *pb.BatchWriteRequest) (*pb.BatchWriteResponse, error)
	BeginTransaction(ctx context.Context, r *pb.BeginTransactionRequest) (*pb.BeginTransactionResponse, error)
	Rollback(ctx context.Context, r *pb.RollbackRequest) error
	// Get reads a document, in a transaction when txn is set; nil, nil when it does not exist.
	Get(ctx context.Context, name string, txn []byte) (*pb.Document, error)
	// Query runs a query and returns its documents and the read time of the result.
	Query(ctx context.Context, r *pb.RunQueryRequest) ([]*pb.Document, time.Time, error)
}

// callError is a call that came back with a status. Any other error is a call whose outcome is
// unknown: the connection broke, or no reply came.
type callError struct {
	Code connect.Code
	Msg  string
	HTTP int
}

func (e *callError) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Msg) }

// definite reports whether err says the write was not applied: a refusal the server decided
// before it changed anything. UNAVAILABLE, INTERNAL, UNKNOWN and DEADLINE_EXCEEDED, like a lost
// connection, leave the outcome open.
func definite(err error) bool {
	var ce *callError
	if !errors.As(err, &ce) {
		return false
	}
	switch ce.Code {
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeAlreadyExists,
		connect.CodeNotFound, connect.CodeAborted, connect.CodePermissionDenied,
		connect.CodeUnauthenticated, connect.CodeResourceExhausted, connect.CodeOutOfRange:
		return true
	}
	return false
}

func codeOf(err error) connect.Code {
	var ce *callError
	if errors.As(err, &ce) {
		return ce.Code
	}
	return 0
}

// grpcAPI calls the Document Database over gRPC (h2c), as the Admin SDKs do.
type grpcAPI struct {
	c    firestorepbconnect.FirestoreClient
	db   string
	auth string // the Authorization header, "" for none
}

func newGRPC(base, db, auth string) *grpcAPI {
	var cp http.Protocols
	cp.SetUnencryptedHTTP2(true)
	hc := &http.Client{Transport: &http.Transport{Protocols: &cp, ResponseHeaderTimeout: 30 * time.Second}}
	return &grpcAPI{c: firestorepbconnect.NewFirestoreClient(hc, base, connect.WithGRPC()), db: db, auth: auth}
}

func (g *grpcAPI) Name() string { return "grpc" }

func req[T any](g *grpcAPI, m *T) *connect.Request[T] {
	r := connect.NewRequest(m)
	r.Header().Set("Google-Cloud-Resource-Prefix", g.db)
	r.Header().Set("X-Goog-Request-Params", "database="+g.db)
	if g.auth != "" {
		r.Header().Set("Authorization", g.auth)
	}
	return r
}

// grpcErr turns a connect error into a callError when the server answered with a status, and
// leaves transport failures as they are.
func grpcErr(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Meta().Get("Content-Type") != "" {
		return &callError{Code: ce.Code(), Msg: ce.Message()}
	}
	return err
}

func (g *grpcAPI) Commit(ctx context.Context, r *pb.CommitRequest) (*pb.CommitResponse, error) {
	resp, err := g.c.Commit(ctx, req(g, r))
	if err != nil {
		return nil, grpcErr(err)
	}
	return resp.Msg, nil
}

func (g *grpcAPI) BatchWrite(ctx context.Context, r *pb.BatchWriteRequest) (*pb.BatchWriteResponse, error) {
	resp, err := g.c.BatchWrite(ctx, req(g, r))
	if err != nil {
		return nil, grpcErr(err)
	}
	return resp.Msg, nil
}

func (g *grpcAPI) BeginTransaction(ctx context.Context, r *pb.BeginTransactionRequest) (*pb.BeginTransactionResponse, error) {
	resp, err := g.c.BeginTransaction(ctx, req(g, r))
	if err != nil {
		return nil, grpcErr(err)
	}
	return resp.Msg, nil
}

func (g *grpcAPI) Rollback(ctx context.Context, r *pb.RollbackRequest) error {
	_, err := g.c.Rollback(ctx, req(g, r))
	return grpcErr(err)
}

func (g *grpcAPI) Get(ctx context.Context, name string, txn []byte) (*pb.Document, error) {
	m := &pb.GetDocumentRequest{Name: name}
	if txn != nil {
		m.ConsistencySelector = &pb.GetDocumentRequest_Transaction{Transaction: txn}
	}
	resp, err := g.c.GetDocument(ctx, req(g, m))
	if err != nil {
		err = grpcErr(err)
		if codeOf(err) == connect.CodeNotFound {
			return nil, nil
		}
		return nil, err
	}
	return resp.Msg, nil
}

func (g *grpcAPI) Query(ctx context.Context, r *pb.RunQueryRequest) ([]*pb.Document, time.Time, error) {
	s, err := g.c.RunQuery(ctx, req(g, r))
	if err != nil {
		return nil, time.Time{}, grpcErr(err)
	}
	defer s.Close()
	var docs []*pb.Document
	var rt time.Time
	for s.Receive() {
		m := s.Msg()
		if m.GetDocument() != nil {
			docs = append(docs, m.GetDocument())
		}
		if m.GetReadTime() != nil {
			rt = m.GetReadTime().AsTime()
		}
	}
	if err := s.Err(); err != nil {
		return nil, time.Time{}, grpcErr(err)
	}
	return docs, rt, nil
}

// restAPI calls the Document Database's REST API (/v1), as the Firestore lite SDK and the REST
// clients do, with the same messages in their JSON form.
type restAPI struct {
	hc   *http.Client
	base string // http://host:port/v1/projects/p/databases/(default)/documents
	auth string
}

func newREST(base, db, auth string) *restAPI {
	return &restAPI{hc: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 16}},
		base: base + "/v1/" + db + "/documents", auth: auth}
}

func (a *restAPI) Name() string { return "rest" }

var pj = protojson.MarshalOptions{}
var pu = protojson.UnmarshalOptions{DiscardUnknown: true}

func (a *restAPI) do(ctx context.Context, method, u string, in, out proto.Message) error {
	var body io.Reader
	if in != nil {
		b, err := pj.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	if a.auth != "" {
		r.Header.Set("Authorization", a.auth)
	}
	resp, err := a.hc.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err // a reply cut short: the outcome is unknown
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Status  string `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(b, &e) != nil || e.Error.Status == "" {
			return fmt.Errorf("HTTP %d without a status: %.200s", resp.StatusCode, b)
		}
		var c connect.Code
		if err := c.UnmarshalText([]byte(strings.ToLower(e.Error.Status))); err != nil {
			return fmt.Errorf("HTTP %d: unknown status %q", resp.StatusCode, e.Error.Status)
		}
		return &callError{Code: c, Msg: e.Error.Message, HTTP: resp.StatusCode}
	}
	if out == nil {
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(string(b)), "[") {
		return fmt.Errorf("unexpected array reply")
	}
	return pu.Unmarshal(b, out)
}

func (a *restAPI) Commit(ctx context.Context, r *pb.CommitRequest) (*pb.CommitResponse, error) {
	out := &pb.CommitResponse{}
	return out, a.do(ctx, http.MethodPost, a.base+":commit", r, out)
}

func (a *restAPI) BatchWrite(ctx context.Context, r *pb.BatchWriteRequest) (*pb.BatchWriteResponse, error) {
	out := &pb.BatchWriteResponse{}
	return out, a.do(ctx, http.MethodPost, a.base+":batchWrite", r, out)
}

func (a *restAPI) BeginTransaction(ctx context.Context, r *pb.BeginTransactionRequest) (*pb.BeginTransactionResponse, error) {
	out := &pb.BeginTransactionResponse{}
	return out, a.do(ctx, http.MethodPost, a.base+":beginTransaction", r, out)
}

func (a *restAPI) Rollback(ctx context.Context, r *pb.RollbackRequest) error {
	return a.do(ctx, http.MethodPost, a.base+":rollback", r, nil)
}

func (a *restAPI) Get(ctx context.Context, name string, txn []byte) (*pb.Document, error) {
	rel := name[strings.Index(name, "/documents/")+len("/documents/"):]
	u := a.base + "/" + rel
	if txn != nil {
		u += "?transaction=" + url.QueryEscape(base64.StdEncoding.EncodeToString(txn))
	}
	out := &pb.Document{}
	err := a.do(ctx, http.MethodGet, u, nil, out)
	if codeOf(err) == connect.CodeNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (a *restAPI) Query(ctx context.Context, r *pb.RunQueryRequest) ([]*pb.Document, time.Time, error) {
	b, err := pj.Marshal(r)
	if err != nil {
		return nil, time.Time{}, err
	}
	u := a.base + ":runQuery"
	if r.Parent != "" && strings.Contains(r.Parent, "/documents/") {
		u = a.base + "/" + r.Parent[strings.Index(r.Parent, "/documents/")+len("/documents/"):] + ":runQuery"
	}
	hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	hr.Header.Set("Content-Type", "application/json")
	if a.auth != "" {
		hr.Header.Set("Authorization", a.auth)
	}
	resp, err := a.hc.Do(hr)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, time.Time{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, time.Time{}, fmt.Errorf("runQuery: HTTP %d: %.300s", resp.StatusCode, body)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, time.Time{}, err
	}
	var docs []*pb.Document
	var rt time.Time
	for _, m := range raw {
		x := &pb.RunQueryResponse{}
		if err := pu.Unmarshal(m, x); err != nil {
			return nil, time.Time{}, err
		}
		if x.GetDocument() != nil {
			docs = append(docs, x.GetDocument())
		}
		if x.GetReadTime() != nil {
			rt = x.GetReadTime().AsTime()
		}
	}
	return docs, rt, nil
}

// Values.

func intV(n int64) *pb.Value  { return &pb.Value{ValueType: &pb.Value_IntegerValue{IntegerValue: n}} }
func strV(s string) *pb.Value { return &pb.Value{ValueType: &pb.Value_StringValue{StringValue: s}} }
func tsV(t time.Time) *pb.Value {
	return &pb.Value{ValueType: &pb.Value_TimestampValue{TimestampValue: timestamppb.New(t)}}
}
func intOf(d *pb.Document, f string) int64 {
	if d == nil {
		return 0
	}
	return d.GetFields()[f].GetIntegerValue()
}

func update(name string, fields map[string]*pb.Value) *pb.Write {
	return &pb.Write{Operation: &pb.Write_Update{Update: &pb.Document{Name: name, Fields: fields}}}
}

func increment(name, field string, by int64) *pb.Write {
	return &pb.Write{Operation: &pb.Write_Transform{Transform: &pb.DocumentTransform{Document: name,
		FieldTransforms: []*pb.DocumentTransform_FieldTransform{{FieldPath: field,
			TransformType: &pb.DocumentTransform_FieldTransform_Increment{Increment: intV(by)}}}}}}
}

func mustNotExist(w *pb.Write) *pb.Write {
	w.CurrentDocument = &pb.Precondition{ConditionType: &pb.Precondition_Exists{Exists: false}}
	return w
}

func mustExist(w *pb.Write) *pb.Write {
	w.CurrentDocument = &pb.Precondition{ConditionType: &pb.Precondition_Exists{Exists: true}}
	return w
}

func del(name string) *pb.Write { return &pb.Write{Operation: &pb.Write_Delete{Delete: name}} }
