package console

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// dataRoutes registers the data browser's routes. Each takes the path it acts on as ?path=,
// relative to the documents root ("users/u1", "users/u1/posts"), never a path segment, so any
// document id reaches it as written.
func (h *handlers) dataRoutes() {
	h.handle("GET", base+"/collections", h.listCollections)
	h.handle("GET", base+"/documents", h.listDocuments)
	h.handle("POST", base+"/documents", h.createDocument)
	h.handle("POST", base+"/documents:recursiveDelete", h.recursiveDelete)
	h.handle("GET", base+"/document", h.getDocument)
	h.handle("PATCH", base+"/document", h.updateDocument)
	h.handle("DELETE", base+"/document", h.deleteDocument)
}

// pathKind is what a ?path= must name.
type pathKind uint8

const (
	parentPath     pathKind = iota + 1 // the root ("") or a document
	collectionPath                     // an odd number of ids
	documentPath                       // an even number of ids, at least two
)

// relPath reads ?path= (or a body's path) as kind: ids joined by "/", none empty. It returns
// the path without the slashes around it.
func relPath(raw string, kind pathKind) (string, error) {
	p := strings.Trim(raw, "/")
	n := 0
	if p != "" {
		ids := strings.Split(p, "/")
		if slices.Contains(ids, "") {
			return "", apierr.Invalid("path", "The path has an empty id.")
		}
		n = len(ids)
	}
	switch kind {
	case parentPath:
		if n%2 == 1 {
			return "", apierr.Invalid("path", "The path must name a document, or be empty for the root.")
		}
	case collectionPath:
		if n%2 == 0 {
			return "", apierr.Invalid("path", "The path must name a collection.")
		}
	case documentPath:
		if n == 0 || n%2 == 1 {
			return "", apierr.Invalid("path", "The path must name a document.")
		}
	}
	return p, nil
}

// name is the full resource name of a relative path ("" is the root).
func (t target) name(rel string) string {
	if rel == "" {
		return t.root()
	}
	return t.root() + "/" + rel
}

// splitCollection is a collection path's parent (a document, or "" for the root) and its id.
func splitCollection(rel string) (parent, id string) {
	i := strings.LastIndexByte(rel, '/')
	if i < 0 {
		return "", rel
	}
	return rel[:i], rel[i+1:]
}

// pageParams reads ?pageSize= (defaultPage when absent, at most maxPage) and ?pageToken=.
func pageParams(r *http.Request) (int32, string, error) {
	q := r.URL.Query()
	size := defaultPage
	if v := q.Get("pageSize"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return 0, "", apierr.Invalid("pageSize", "pageSize must be a positive number.")
		}
		size = min(n, maxPage)
	}
	return int32(size), q.Get("pageToken"), nil
}

// documentJSON is a document as the dashboard shows it: the REST Document (name, fields in
// Firestore's REST Value form, createTime, updateTime) with its path below the root, its id,
// and missing: a document that does not exist but has subcollections, which a listing names
// alone, as the Firebase console shows it in italics.
func (t target) documentJSON(d *pb.Document) (map[string]json.RawMessage, error) {
	b, err := wire.MarshalREST(d)
	if err != nil {
		return nil, err
	}
	out := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	rel := strings.TrimPrefix(d.GetName(), t.root()+"/")
	id := rel[strings.LastIndexByte(rel, '/')+1:]
	out["path"], _ = json.Marshal(rel)
	out["id"], _ = json.Marshal(id)
	out["missing"], _ = json.Marshal(d.GetCreateTime() == nil)
	if _, ok := out["fields"]; !ok && d.GetCreateTime() != nil {
		out["fields"] = json.RawMessage("{}")
	}
	return out, nil
}

// listCollections returns a page of the collection ids directly under ?path= (a document, or
// the root when empty), in order, whether or not that document exists.
func (h *handlers) listCollections(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rel, err := relPath(r.URL.Query().Get("path"), parentPath)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	size, token, err := pageParams(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	res, err := h.svc.ListCollectionIds(r.Context(), t.call, &pb.ListCollectionIdsRequest{
		Parent: t.name(rel), PageSize: size, PageToken: token,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := map[string]any{"collectionIds": res.GetCollectionIds()}
	if res.GetCollectionIds() == nil {
		out["collectionIds"] = []string{}
	}
	if res.GetNextPageToken() != "" {
		out["nextPageToken"] = res.GetNextPageToken()
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// listDocuments returns a page of the collection at ?path=, in id order (the Firebase console's),
// with the missing documents that only have subcollections among them, flagged. ?fields=false
// leaves the fields out, for a list of ids alone.
func (h *handlers) listDocuments(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rel, err := relPath(r.URL.Query().Get("path"), collectionPath)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	size, token, err := pageParams(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	parent, id := splitCollection(rel)
	req := &pb.ListDocumentsRequest{Parent: t.name(parent), CollectionId: id, PageSize: size, PageToken: token, ShowMissing: true}
	if r.URL.Query().Get("fields") == "false" {
		req.Mask = &pb.DocumentMask{}
	}
	res, err := h.svc.ListDocuments(r.Context(), t.call, req)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	docs := make([]map[string]json.RawMessage, 0, len(res.GetDocuments()))
	for _, d := range res.GetDocuments() {
		j, err := t.documentJSON(d)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		docs = append(docs, j)
	}
	out := map[string]any{"documents": docs}
	if res.GetNextPageToken() != "" {
		out["nextPageToken"] = res.GetNextPageToken()
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (h *handlers) getDocument(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rel, err := relPath(r.URL.Query().Get("path"), documentPath)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	d, err := h.svc.GetDocument(r.Context(), t.call, &pb.GetDocumentRequest{Name: t.name(rel)})
	h.writeDocument(w, r, t, d, err)
}

func (h *handlers) writeDocument(w http.ResponseWriter, r *http.Request, t target, d *pb.Document, err error) {
	if err != nil {
		h.fail(w, r, err)
		return
	}
	j, err := t.documentJSON(d)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, j)
}

// documentBody is a write's body. fields is a map of field names to values in Firestore's REST
// form, as a REST Document carries it.
type documentBody struct {
	// DocumentID names a new document; empty, the server makes one (20 characters, as the SDKs).
	DocumentID string          `json:"documentId"`
	Fields     json.RawMessage `json:"fields"`
	// UpdateMask lists the field paths an edit sets: a path named here and absent from fields
	// is deleted, and a field not named is kept. Absent, the edit replaces every field.
	UpdateMask []string `json:"updateMask"`
	// Exists, when set, is the edit's precondition: true for a document that must exist.
	Exists *bool `json:"exists"`
}

// fieldsOf reads a body's fields as the REST API reads a Document's, with its errors' texts.
func fieldsOf(raw json.RawMessage) (map[string]*pb.Value, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var d pb.Document
	body := append(append([]byte(`{"fields":`), raw...), '}')
	if err := wire.UnmarshalREST(body, &d); err != nil {
		return nil, err
	}
	return d.GetFields(), nil
}

// createDocument creates a document in the collection at ?path=, with the body's id or one the
// server makes. A document that exists already is ALREADY_EXISTS.
func (h *handlers) createDocument(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rel, err := relPath(r.URL.Query().Get("path"), collectionPath)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in documentBody
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	fields, err := fieldsOf(in.Fields)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	parent, id := splitCollection(rel)
	d, err := h.svc.CreateDocument(r.Context(), t.call, &pb.CreateDocumentRequest{
		Parent: t.name(parent), CollectionId: id, DocumentId: in.DocumentID, Document: &pb.Document{Fields: fields},
	})
	if err == nil {
		err = h.audit(r, t, "documents.document.create", strings.TrimPrefix(d.GetName(), t.root()+"/"),
			map[string]any{"fields": len(fields)})
	}
	h.writeDocument(w, r, t, d, err)
}

// updateDocument sets the fields of the document at ?path=, creating it unless the body's
// exists says it must exist: every field, or with updateMask those it names alone, so a field
// named and not given is deleted (UpdateDocument's mask, as the REST API's PATCH).
func (h *handlers) updateDocument(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rel, err := relPath(r.URL.Query().Get("path"), documentPath)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in documentBody
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	if in.DocumentID != "" {
		h.m.Fail(w, r, apierr.Invalid("documentId", "An edit names its document by its path."))
		return
	}
	fields, err := fieldsOf(in.Fields)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	req := &pb.UpdateDocumentRequest{Document: &pb.Document{Name: t.name(rel), Fields: fields}}
	if in.UpdateMask != nil {
		req.UpdateMask = &pb.DocumentMask{FieldPaths: in.UpdateMask}
	}
	if in.Exists != nil {
		req.CurrentDocument = &pb.Precondition{ConditionType: &pb.Precondition_Exists{Exists: *in.Exists}}
	}
	d, err := h.svc.UpdateDocument(r.Context(), t.call, req)
	if err == nil {
		details := map[string]any{"fields": len(fields)}
		if in.UpdateMask != nil {
			details["mask"] = len(in.UpdateMask)
		}
		err = h.audit(r, t, "documents.document.update", rel, details)
	}
	h.writeDocument(w, r, t, d, err)
}

// deleteDocument deletes the document at ?path=, and not its subcollections: the document
// becomes missing when it has any. Deleting a document that does not exist is no change.
func (h *handlers) deleteDocument(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rel, err := relPath(r.URL.Query().Get("path"), documentPath)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	if _, err := h.svc.DeleteDocument(r.Context(), t.call, &pb.DeleteDocumentRequest{Name: t.name(rel)}); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.audit(r, t, "documents.document.delete", rel, nil); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct{}{})
}

// The bounds of a recursive delete. One request deletes at most maxRecursiveDelete documents
// and reports whether any remain, so the dashboard repeats it until done: a large tree never
// holds one request for long. Each commit deletes deleteBatch documents, firebase-tools'
// deleteBatchSize.
var maxRecursiveDelete = 5000 // a variable for the tests alone

const deleteBatch = 250

// minID is the least document id in Firestore's order, firebase-tools' MIN_ID: the bounds of a
// collection's descendants start at it.
const minID = "__id-9223372036854775808__"

// recursiveDelete deletes a document and everything under it, or a collection and everything
// under it, the way firebase firestore:delete -r does: the document first, then the
// descendants a kindless all-descendants query names (the collection's bounded by name, as
// collectionDescendantsQuery bounds them), in commits of deleteBatch deletes. It answers how many
// documents it deleted, and done once none remain.
func (h *handlers) recursiveDelete(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in struct {
		Path string `json:"path"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	rel, err := relPath(in.Path, parentPath)
	isDoc := err == nil && rel != ""
	if !isDoc {
		if rel, err = relPath(in.Path, collectionPath); err != nil {
			h.m.Fail(w, r, apierr.Invalid("path", "The path must name a document or a collection."))
			return
		}
	}
	deleted := 0
	if isDoc {
		n, err := h.deleteIfExists(r, t, rel)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		deleted += n
	}
	done := false
	for deleted < maxRecursiveDelete {
		want := min(deleteBatch, maxRecursiveDelete-deleted)
		names, err := h.descendants(r, t, rel, isDoc, want)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		if len(names) > 0 {
			if err := h.deleteAll(r, t, names); err != nil {
				h.fail(w, r, err)
				return
			}
			deleted += len(names)
		}
		if len(names) < want {
			done = true
			break
		}
	}
	if !done {
		more, err := h.descendants(r, t, rel, isDoc, 1)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		done = len(more) == 0
	}
	if deleted > 0 {
		if err := h.audit(r, t, "documents.recursive_delete", rel, map[string]any{"deleted": deleted, "done": done}); err != nil {
			h.m.Fail(w, r, err)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "done": done})
}

// deleteIfExists deletes a document, and counts it when it existed.
func (h *handlers) deleteIfExists(r *http.Request, t target, rel string) (int, error) {
	_, err := h.svc.GetDocument(r.Context(), t.call, &pb.GetDocumentRequest{Name: t.name(rel), Mask: &pb.DocumentMask{}})
	switch {
	case err != nil && apierr.CodeOf(docError(err)) == apierr.NotFound:
		return 0, nil
	case err != nil:
		return 0, err
	}
	return 1, h.deleteAll(r, t, []string{t.name(rel)})
}

// descendants names up to limit documents below a document, or in and below a collection, in
// name order, with firebase-tools' queries (docDescendantsQuery, collectionDescendantsQuery).
func (h *handlers) descendants(r *http.Request, t target, rel string, isDoc bool, limit int) ([]string, error) {
	sq := map[string]any{
		"from":    []any{map[string]any{"allDescendants": true}},
		"select":  map[string]any{"fields": []any{map[string]any{"fieldPath": "__name__"}}},
		"orderBy": []any{map[string]any{"field": map[string]any{"fieldPath": "__name__"}}},
		"limit":   limit,
	}
	parent := t.name(rel)
	if !isDoc {
		p, _ := splitCollection(rel)
		parent = t.name(p)
		bound := func(op, ref string) map[string]any {
			return map[string]any{"fieldFilter": map[string]any{
				"field": map[string]any{"fieldPath": "__name__"}, "op": op, "value": map[string]any{"referenceValue": ref},
			}}
		}
		sq["where"] = map[string]any{"compositeFilter": map[string]any{"op": "AND", "filters": []any{
			bound("GREATER_THAN_OR_EQUAL", t.name(rel)+"/"+minID),
			bound("LESS_THAN", t.name(rel)+"\x00/"+minID),
		}}}
	}
	body, err := json.Marshal(map[string]any{"structuredQuery": sq})
	if err != nil {
		return nil, err
	}
	req := &pb.RunQueryRequest{}
	if err := wire.UnmarshalREST(body, req); err != nil {
		return nil, err
	}
	req.Parent = parent
	var names []string
	err = h.svc.RunQuery(r.Context(), t.call, req, func(res *pb.RunQueryResponse) error {
		if d := res.GetDocument(); d != nil {
			names = append(names, d.GetName())
		}
		return nil
	})
	return names, err
}

// deleteAll deletes the named documents in one commit.
func (h *handlers) deleteAll(r *http.Request, t target, names []string) error {
	writes := make([]*pb.Write, 0, len(names))
	for _, n := range names {
		writes = append(writes, &pb.Write{Operation: &pb.Write_Delete{Delete: n}})
	}
	_, err := h.svc.Commit(r.Context(), t.call, &pb.CommitRequest{Database: t.database(), Writes: writes})
	return err
}
