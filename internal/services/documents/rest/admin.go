package rest

import (
	"bytes"
	"io"
	"net/http"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/wire"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/longrunningpb"
)

// The Admin API's REST bindings, as google.firestore.admin.v1 and google.longrunning declare
// them under /v1, for the calls firebase-tools and gcloud make (ADR-025 §4.5):
//
//	GET    projects/{p}/databases                                          ListDatabases
//	GET    projects/{p}/databases/{d}                                      GetDatabase
//	POST   projects/{p}/databases/{d}/collectionGroups/{g}/indexes         CreateIndex (body: index)
//	GET    projects/{p}/databases/{d}/collectionGroups/{g}/indexes         ListIndexes ({g} may be -)
//	GET    projects/{p}/databases/{d}/collectionGroups/{g}/indexes/{id}    GetIndex
//	DELETE projects/{p}/databases/{d}/collectionGroups/{g}/indexes/{id}    DeleteIndex
//	GET    projects/{p}/databases/{d}/collectionGroups/{g}/fields          ListFields (?filter=)
//	GET    projects/{p}/databases/{d}/collectionGroups/{g}/fields/{path}   GetField
//	PATCH  projects/{p}/databases/{d}/collectionGroups/{g}/fields/{path}   UpdateField (body: field, ?updateMask=)
//	GET    projects/{p}/databases/{d}/operations                           ListOperations
//	GET    projects/{p}/databases/{d}/operations/{id}                      GetOperation
//	GET    projects/{p}/databases/{d}/backupSchedules                      ListBackupSchedules (none)
//
// A field path may be quoted with backticks and hold any character but '/', percent-encoded
// as the client sends it. Every other method or path of the Admin API (creating, updating and
// deleting a database, backups, exports and imports, cancelling an operation, …) is
// UNIMPLEMENTED. The Admin API is not open to browsers (ADR-027 §5.2): no CORS headers.

// errAdminUnserved is the Admin API's answer to what this server does not serve.
var errAdminUnserved = wire.Unimplemented("This method of the Document Database's Admin API")

// admin serves a path of the surface outside documents: the Admin API.
func (h *Handler) admin(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	segs := strings.Split(name, "/")
	// segs: projects {p} databases [{d} [collectionGroups {g} indexes|fields [{id}…]] | [operations [{id}]]]
	// A field path holds any character in backticks, '/' and ':' too: it is the rest of the path.
	field := len(segs) >= 8 && segs[4] == "collectionGroups" && segs[6] == "fields"
	if field {
		segs = append(segs[:7], strings.Join(segs[7:], "/"))
	}
	// A ':' elsewhere is a custom method (:exportDocuments, :cancel, …), none of them served.
	if len(segs) < 3 || segs[0] != "projects" || segs[2] != "databases" || strings.Contains(strings.Join(segs[:min(len(segs), 7)], "/"), ":") {
		wire.WriteREST(w, errAdminUnserved, false)
		return
	}
	get, method := r.Method == http.MethodGet, r.Method
	switch {
	case len(segs) == 3 && get:
		req := &adminpb.ListDatabasesRequest{}
		if query(w, r, req) {
			req.Parent = "projects/" + segs[1]
			res, err := h.svc.ListDatabases(r.Context(), call(r), req)
			write(w, res, err)
		}
	case len(segs) == 4 && get:
		req := &adminpb.GetDatabaseRequest{}
		if query(w, r, req) {
			req.Name = name
			res, err := h.svc.GetDatabase(r.Context(), call(r), req)
			write(w, res, err)
		}
	case len(segs) == 5 && segs[4] == "operations" && get:
		req := &longrunningpb.ListOperationsRequest{}
		if query(w, r, req) {
			req.Name = strings.Join(segs[:4], "/")
			res, err := h.svc.ListOperations(r.Context(), call(r), req)
			write(w, res, err)
		}
	case len(segs) == 5 && segs[4] == "backupSchedules" && get:
		req := &adminpb.ListBackupSchedulesRequest{}
		if query(w, r, req) {
			req.Parent = strings.Join(segs[:4], "/")
			res, err := h.svc.ListBackupSchedules(r.Context(), call(r), req)
			write(w, res, err)
		}
	case len(segs) == 6 && segs[4] == "operations" && get:
		req := &longrunningpb.GetOperationRequest{}
		if query(w, r, req) {
			req.Name = name
			res, err := h.svc.GetOperation(r.Context(), call(r), req)
			write(w, res, err)
		}
	case len(segs) == 7 && segs[4] == "collectionGroups" && segs[6] == "indexes" && method == http.MethodPost:
		req := &adminpb.CreateIndexRequest{}
		if wrapped(w, r, req, "index", "parent") && query(w, r, req) {
			req.Parent = strings.Join(segs[:6], "/")
			res, err := h.svc.CreateIndex(r.Context(), call(r), req)
			write(w, res, err)
		}
	case len(segs) == 7 && segs[4] == "collectionGroups" && segs[6] == "indexes" && get:
		req := &adminpb.ListIndexesRequest{}
		if query(w, r, req) {
			req.Parent = strings.Join(segs[:6], "/")
			res, err := h.svc.ListIndexes(r.Context(), call(r), req)
			write(w, res, err)
		}
	case len(segs) == 8 && segs[4] == "collectionGroups" && segs[6] == "indexes" && get:
		req := &adminpb.GetIndexRequest{}
		if query(w, r, req) {
			req.Name = name
			res, err := h.svc.GetIndex(r.Context(), call(r), req)
			write(w, res, err)
		}
	case len(segs) == 8 && segs[4] == "collectionGroups" && segs[6] == "indexes" && method == http.MethodDelete:
		req := &adminpb.DeleteIndexRequest{}
		if query(w, r, req) {
			req.Name = name
			res, err := h.svc.DeleteIndex(r.Context(), call(r), req)
			write(w, res, err)
		}
	case len(segs) == 7 && segs[4] == "collectionGroups" && segs[6] == "fields" && get:
		req := &adminpb.ListFieldsRequest{}
		if query(w, r, req) {
			req.Parent = strings.Join(segs[:6], "/")
			res, err := h.svc.ListFields(r.Context(), call(r), req)
			write(w, res, err)
		}
	case len(segs) == 8 && segs[4] == "collectionGroups" && segs[6] == "fields" && get:
		req := &adminpb.GetFieldRequest{}
		if query(w, r, req) {
			req.Name = name
			res, err := h.svc.GetField(r.Context(), call(r), req)
			write(w, res, err)
		}
	case len(segs) == 8 && segs[4] == "collectionGroups" && segs[6] == "fields" && method == http.MethodPatch:
		req := &adminpb.UpdateFieldRequest{}
		if wrapped(w, r, req, "field") && query(w, r, req) {
			// The path names the field, whatever the body says, as UpdateDocument's does.
			if req.Field == nil {
				req.Field = &adminpb.Field{}
			}
			req.Field.Name = name
			res, err := h.svc.UpdateField(r.Context(), call(r), req)
			write(w, res, err)
		}
	case adminResource(segs):
		wire.WriteREST(w, errNotFound, false)
	default:
		wire.WriteREST(w, errAdminUnserved, false)
	}
}

// adminResource reports whether segs is one of the served resources, under another method:
// NOT_FOUND, as the data paths answer a method no binding has.
func adminResource(segs []string) bool {
	switch {
	case len(segs) == 5 && segs[4] == "operations", len(segs) == 6 && segs[4] == "operations":
		return true
	case len(segs) == 6 && segs[4] == "collectionGroups":
		// A collection group is no resource of its own: Google's front end answers its HTML
		// 404 page (f3-admin.json database.get_missing_collection_group), this the JSON one.
		return true
	case len(segs) == 7 || len(segs) == 8:
		return segs[4] == "collectionGroups" && (segs[6] == "indexes" || segs[6] == "fields")
	}
	return false
}

// query binds the request's query parameters to req, or answers their error.
func query(w http.ResponseWriter, r *http.Request, req proto.Message) bool {
	if err := wire.UnmarshalQuery(r.URL.Query(), req); err != nil {
		write(w, nil, err)
		return false
	}
	return true
}

// wrapped reads a body that is one field of req (the bindings' body: "index", body: "field"),
// under that field, so an error names its path from req (index.fields[0].order …), or answers
// its error.
func wrapped(w http.ResponseWriter, r *http.Request, req proto.Message, field string, pathFields ...string) bool {
	b, err := io.ReadAll(r.Body)
	if err != nil {
		write(w, nil, bodyError(err))
		return false
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return true
	}
	b = append(append([]byte(`{"`+field+`":`), b...), '}')
	if err := wire.UnmarshalREST(b, req, pathFields...); err != nil {
		write(w, nil, err)
		return false
	}
	return true
}
