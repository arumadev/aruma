// Package rest is the Document Database's REST surface under /v1/ (ADR-027 §5): hand-written
// handlers that read production's JSON into the same google.firestore.v1 request messages the
// gRPC surface receives (wire.UnmarshalREST, wire.UnmarshalQuery), call package api, and answer
// in production's JSON (wire.MarshalREST), errors in Google's form, array-wrapped on the
// streaming methods (wire.WriteREST).
//
// It owns /v1/projects/{p}/databases and everything under it, ahead of the Management API
// mounted on /v1/projects/ (ADR-027 §5.1, the one declared exception of the route-ownership
// test), and the WebChannel paths /google.firestore.v1.Firestore/{Listen,Write}/channel. The
// data paths, documents and below, and the WebChannel paths are open to every origin; the Admin
// API's are not (ADR-027 §5.2).
package rest

import (
	"bytes"
	"cmp"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// Prefix is the Management API's /v1/projects/ prefix, under which the Document Database claims
// {p}/databases first.
const Prefix = "/v1/projects/"

// ChannelPaths are WebChannel's paths (F6), reserved here since F0.
var ChannelPaths = []string{"/google.firestore.v1.Firestore/Listen/channel", "/google.firestore.v1.Firestore/Write/channel"}

// Owns reports whether a path belongs to the Document Database's REST or WebChannel surface:
// /v1/projects/{p}/databases[/…], or a WebChannel path.
func Owns(path string) bool {
	for _, c := range ChannelPaths {
		if path == c {
			return true
		}
	}
	rest, ok := strings.CutPrefix(path, Prefix)
	if !ok {
		return false
	}
	_, rest, ok = strings.Cut(rest, "/")
	return ok && (rest == "databases" || strings.HasPrefix(rest, "databases/") || strings.HasPrefix(rest, "databases:"))
}

// Options configure the handler.
type Options struct {
	Service *api.Service
	Logger  *slog.Logger
}

// Handler serves the REST surface. The body cap is the mount's (documents.limits.message_bytes).
type Handler struct {
	svc *api.Service
	log *slog.Logger
}

// New returns the handler.
func New(o Options) *Handler {
	return &Handler{svc: o.Service, log: cmp.Or(o.Logger, slog.Default())}
}

// verbs are the custom methods of the documents paths, by their name after ':'.
var verbs = map[string]string{
	"batchGet": "BatchGetDocuments", "listCollectionIds": "ListCollectionIds", "commit": "Commit",
	"beginTransaction": "BeginTransaction", "rollback": "Rollback", "runQuery": "RunQuery",
	"runAggregationQuery": "RunAggregationQuery", "partitionQuery": "PartitionQuery",
	"batchWrite": "BatchWrite", "executePipeline": "ExecutePipeline", "listen": "Listen", "write": "Write",
}

// streaming are the methods REST answers with an array (ADR-027 §5.1).
var streaming = map[string]bool{"BatchGetDocuments": true, "RunQuery": true, "RunAggregationQuery": true, "ExecutePipeline": true}

// errNotFound answers a path or method no binding of the API matches. Provisional text.
var errNotFound = wire.NewError(wire.CodeNotFound, "Requested entity was not found.")

// route is a request on a documents path, split as the API's bindings read it.
type route struct {
	database string // projects/{p}/databases/{d}
	rel      []string
	verb     string // the method a custom verb names, or ""
}

func (rt route) name() string {
	if len(rt.rel) == 0 {
		return rt.database + "/documents"
	}
	return rt.database + "/documents/" + strings.Join(rt.rel, "/")
}

// ServeHTTP routes a request of the surface.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	for _, c := range ChannelPaths {
		if path == c {
			if cors(w, r) {
				return
			}
			wire.WriteREST(w, wire.Unimplemented("WebChannel"), false)
			return
		}
	}
	rt, ok := parse(path)
	if !ok {
		// The Admin API (databases, indexes, fields, operations) comes in F3, closed to
		// browsers like every management path.
		wire.WriteREST(w, wire.Unimplemented("The Document Database's Admin API"), false)
		return
	}
	if cors(w, r) {
		return
	}
	switch {
	case rt.verb == "BatchGetDocuments" && r.Method == http.MethodPost && len(rt.rel) == 0:
		h.batchGet(w, r, rt)
	case rt.verb == "ListCollectionIds" && r.Method == http.MethodPost:
		h.listCollectionIds(w, r, rt)
	case rt.verb != "":
		h.unserved(w, r, rt)
	case len(rt.rel) == 0:
		wire.WriteREST(w, errNotFound, false)
	case r.Method == http.MethodGet && len(rt.rel)%2 == 0:
		h.get(w, r, rt)
	case r.Method == http.MethodGet:
		h.list(w, r, rt)
	case r.Method == http.MethodPost && len(rt.rel)%2 == 1:
		h.create(w, r, rt)
	case r.Method == http.MethodPatch:
		h.update(w, r, rt)
	case r.Method == http.MethodDelete:
		h.delete(w, r, rt)
	default:
		wire.WriteREST(w, errNotFound, false)
	}
}

// parse splits a documents path: /v1/projects/{p}/databases/{d}/documents[/{rel}][:{verb}].
// Any other path of the surface is the Admin API's: ok is false. A trailing slash is ignored,
// as production ignores it (f1-list-documents.json trailing_slash_*).
func parse(path string) (route, bool) {
	tail := strings.TrimPrefix(path, Prefix)
	project, tail, _ := strings.Cut(tail, "/")
	tail, ok := strings.CutPrefix(tail, "databases/")
	if !ok {
		return route{}, false
	}
	database, tail, _ := strings.Cut(tail, "/")
	docs, ok := strings.CutPrefix(tail, "documents")
	if !ok || docs != "" && docs[0] != '/' && docs[0] != ':' {
		return route{}, false
	}
	rt := route{database: "projects/" + project + "/databases/" + database}
	if i := strings.LastIndexByte(docs, ':'); i >= 0 && !strings.Contains(docs[i:], "/") {
		if m, ok := verbs[docs[i+1:]]; ok {
			rt.verb, docs = m, docs[:i]
		}
	}
	docs = strings.TrimSuffix(strings.TrimPrefix(docs, "/"), "/")
	if docs != "" {
		rt.rel = strings.Split(docs, "/")
	}
	return rt, true
}

// unservedBodies are the request messages of the custom methods of later phases, with the
// field their path fills.
var unservedBodies = map[string]struct {
	msg       func() proto.Message
	pathField string
}{
	"Commit":              {func() proto.Message { return &pb.CommitRequest{} }, "database"},
	"BeginTransaction":    {func() proto.Message { return &pb.BeginTransactionRequest{} }, "database"},
	"Rollback":            {func() proto.Message { return &pb.RollbackRequest{} }, "database"},
	"BatchWrite":          {func() proto.Message { return &pb.BatchWriteRequest{} }, "database"},
	"RunQuery":            {func() proto.Message { return &pb.RunQueryRequest{} }, "parent"},
	"RunAggregationQuery": {func() proto.Message { return &pb.RunAggregationQueryRequest{} }, "parent"},
	"PartitionQuery":      {func() proto.Message { return &pb.PartitionQueryRequest{} }, "parent"},
}

// unserved answers a custom method of a later phase: UNIMPLEMENTED, after the front end's
// reading of its body, whose errors production answers before the method runs
// (f1-wire-errors.json json.unknown_nested_field, json.bad_enum, oneof.*).
func (h *Handler) unserved(w http.ResponseWriter, r *http.Request, rt route) {
	if r.Method == http.MethodPost {
		if b, ok := unservedBodies[rt.verb]; ok {
			if err := body(r, b.msg(), b.pathField); err != nil {
				wire.WriteREST(w, err, streaming[rt.verb])
				return
			}
		}
	}
	wire.WriteREST(w, wire.Unimplemented(rt.verb), streaming[rt.verb])
}

// call is how a REST request arrived, for package api.
func call(r *http.Request) api.Call {
	return api.Call{
		Authorization:  r.Header.Get("Authorization"),
		Transport:      authn.TransportOf(r),
		ResourcePrefix: r.Header.Get("Google-Cloud-Resource-Prefix"),
	}
}

// body reads a request body, JSON whatever its Content-Type says (text/plain from the web SDK,
// ADR-027 §5.4), into m.
func body(r *http.Request, m proto.Message, pathFields ...string) error {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return bodyError(err)
	}
	return wire.UnmarshalREST(b, m, pathFields...)
}

// bodyError is a body that could not be read: over the cap, or cut short.
func bodyError(err error) error {
	if mbe, ok := errors.AsType[*http.MaxBytesError](err); ok {
		// Google's front end's text for a body over its limit (provisional).
		return wire.InvalidArgument("Request payload size exceeds the limit: " + strconv.FormatInt(mbe.Limit, 10) + " bytes.")
	}
	return wire.InvalidArgument("Invalid JSON payload received. Unexpected end of input.")
}

// document reads a body that is a request's document (body: "document" in the API's bindings),
// as the front end does: under the request's document field, so an error names its path from
// there (document.fields[0].value…).
func document(r *http.Request) (*pb.Document, error) {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, bodyError(err)
	}
	var wrapped pb.UpdateDocumentRequest
	if len(bytes.TrimSpace(b)) > 0 {
		b = append(append([]byte(`{"document":`), b...), '}')
		if err := wire.UnmarshalREST(b, &wrapped); err != nil {
			return nil, err
		}
	}
	if wrapped.Document == nil {
		return &pb.Document{}, nil
	}
	return wrapped.Document, nil
}

// write answers m, or err in Google's form.
func write(w http.ResponseWriter, m proto.Message, err error) {
	if err != nil {
		wire.WriteREST(w, err, false)
		return
	}
	b, err := wire.MarshalREST(m)
	if err != nil {
		wire.WriteREST(w, err, false)
		return
	}
	writeJSON(w, b)
}

func writeJSON(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", wire.RESTContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, rt route) {
	req := &pb.GetDocumentRequest{}
	if err := wire.UnmarshalQuery(r.URL.Query(), req); err != nil {
		write(w, nil, err)
		return
	}
	req.Name = rt.name()
	res, err := h.svc.GetDocument(r.Context(), call(r), req)
	write(w, res, err)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request, rt route) {
	req := &pb.ListDocumentsRequest{}
	if err := wire.UnmarshalQuery(r.URL.Query(), req); err != nil {
		write(w, nil, err)
		return
	}
	parent := route{database: rt.database, rel: rt.rel[:len(rt.rel)-1]}
	req.Parent, req.CollectionId = parent.name(), rt.rel[len(rt.rel)-1]
	res, err := h.svc.ListDocuments(r.Context(), call(r), req)
	write(w, res, err)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request, rt route) {
	req := &pb.CreateDocumentRequest{}
	if err := wire.UnmarshalQuery(r.URL.Query(), req); err != nil {
		write(w, nil, err)
		return
	}
	doc, err := document(r)
	if err != nil {
		write(w, nil, err)
		return
	}
	parent := route{database: rt.database, rel: rt.rel[:len(rt.rel)-1]}
	req.Parent, req.CollectionId, req.Document = parent.name(), rt.rel[len(rt.rel)-1], doc
	res, err := h.svc.CreateDocument(r.Context(), call(r), req)
	write(w, res, err)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request, rt route) {
	req := &pb.UpdateDocumentRequest{}
	if err := wire.UnmarshalQuery(r.URL.Query(), req); err != nil {
		write(w, nil, err)
		return
	}
	doc, err := document(r)
	if err != nil {
		write(w, nil, err)
		return
	}
	// The path names the document, whatever the body says (f1-update.json name_in_body_differs).
	doc.Name = rt.name()
	req.Document = doc
	res, err := h.svc.UpdateDocument(r.Context(), call(r), req)
	write(w, res, err)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request, rt route) {
	req := &pb.DeleteDocumentRequest{}
	if err := wire.UnmarshalQuery(r.URL.Query(), req); err != nil {
		write(w, nil, err)
		return
	}
	req.Name = rt.name()
	res, err := h.svc.DeleteDocument(r.Context(), call(r), req)
	write(w, res, err)
}

func (h *Handler) listCollectionIds(w http.ResponseWriter, r *http.Request, rt route) {
	req := &pb.ListCollectionIdsRequest{}
	if err := body(r, req, "parent"); err != nil {
		write(w, nil, err)
		return
	}
	req.Parent = rt.name()
	res, err := h.svc.ListCollectionIds(r.Context(), call(r), req)
	write(w, res, err)
}

// batchGet answers an array of responses, or an array holding the error (ADR-027 §5.1). The
// responses are written as package api sends them, so the answer is never held whole: an error
// after the first is the array's last element, the status already sent.
func (h *Handler) batchGet(w http.ResponseWriter, r *http.Request, rt route) {
	req := &pb.BatchGetDocumentsRequest{}
	if err := body(r, req, "database"); err != nil {
		wire.WriteREST(w, err, true)
		return
	}
	req.Database = rt.database
	started := false
	err := h.svc.BatchGetDocuments(r.Context(), call(r), req, func(res *pb.BatchGetDocumentsResponse) error {
		b, err := wire.MarshalREST(res)
		if err != nil {
			return err
		}
		sep := byte(',')
		if !started {
			w.Header().Set("Content-Type", wire.RESTContentType)
			w.WriteHeader(http.StatusOK)
			started, sep = true, '['
		}
		_, err = w.Write(append([]byte{sep}, b...))
		return err
	})
	switch {
	case !started && err != nil:
		wire.WriteREST(w, err, true)
	case !started:
		writeJSON(w, []byte("[]"))
	case err != nil:
		_, _ = w.Write(append(append([]byte{','}, wire.From(err).RESTBody()...), ']'))
	default:
		_, _ = w.Write([]byte{']'})
	}
}
