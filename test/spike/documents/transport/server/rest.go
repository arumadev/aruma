package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// rest serves the two REST calls the Node Admin SDK makes with preferRest for the spike's
// scenarios: :commit and :batchGet (a server-streaming call, answered as one JSON array). Both
// stacks need this by hand: neither transcodes google.api.http.
func (f *fake) rest() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/projects/{project}/databases/{database}/{call}", func(w http.ResponseWriter, r *http.Request) {
		database := "projects/" + r.PathValue("project") + "/databases/" + r.PathValue("database")
		body, err := io.ReadAll(io.LimitReader(r.Body, maxMessage))
		if err != nil {
			restError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
			return
		}
		out := protojson.MarshalOptions{UseEnumNumbers: strings.Contains(r.URL.RawQuery, "enum-encoding=int")}
		switch r.PathValue("call") {
		case "documents:commit":
			req := &pb.CommitRequest{}
			if err := protojson.Unmarshal(body, req); err != nil {
				restError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
				return
			}
			req.Database = cmp.Or(req.Database, database)
			if indexError(req) {
				restError(w, http.StatusBadRequest, "FAILED_PRECONDITION", indexMessage)
				return
			}
			writeJSON(w, out, f.commit(req))
		case "documents:batchGet":
			req := &pb.BatchGetDocumentsRequest{}
			if err := protojson.Unmarshal(body, req); err != nil {
				restError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
				return
			}
			var buf bytes.Buffer
			buf.WriteByte('[')
			err := f.batchGet(req, func(res *pb.BatchGetDocumentsResponse) error {
				if buf.Len() > 1 {
					buf.WriteByte(',')
				}
				b, err := out.Marshal(res)
				buf.Write(b)
				return err
			})
			if err != nil {
				restError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
				return
			}
			buf.WriteByte(']')
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Write(buf.Bytes())
		default:
			restError(w, http.StatusNotImplemented, "UNIMPLEMENTED", "The spike serves :commit and :batchGet only.")
		}
	})
	return mux
}

func writeJSON(w http.ResponseWriter, opts protojson.MarshalOptions, m proto.Message) {
	b, err := opts.Marshal(m)
	if err != nil {
		restError(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(b)
}

// restError writes Google's JSON error body.
func restError(w http.ResponseWriter, code int, status, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "status": status}})
}
