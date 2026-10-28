package console

import (
	"encoding/json"
	"net/http"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/longrunningpb"
)

// indexRoutes registers the indexes screen's routes: composite indexes, single-field
// exemptions (the fields whose indexes are their own, not their ancestor's) and TTL policies,
// through the Admin API's methods, so their checks, states and build operations are the ones
// firebase deploy sees. Indexes and fields travel in the Admin API's REST form.
func (h *handlers) indexRoutes() {
	h.handle("GET", base+"/indexes", h.listIndexes)
	h.handle("POST", base+"/indexes", h.createIndex)
	h.handle("DELETE", base+"/indexes/{group}/{id}", h.deleteIndex)
	h.handle("GET", base+"/fields", h.listOverrides)
	h.handle("PUT", base+"/fields", h.setOverride)
	h.handle("DELETE", base+"/fields", h.clearOverride)
	h.handle("GET", base+"/ttl", h.listTTL)
	h.handle("PUT", base+"/ttl", h.setTTL)
	h.handle("DELETE", base+"/ttl", h.clearTTL)
	h.handle("GET", base+"/operations", h.listOperations)
}

// group is a collection group's name, projects/{p}/databases/(default)/collectionGroups/{g};
// "-" stands for every group in a listing.
func (t target) group(g string) string { return t.database() + "/collectionGroups/" + g }

// restList encodes messages in their REST form.
func restList[M proto.Message](ms []M) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(ms))
	for _, m := range ms {
		b, err := wire.MarshalREST(m)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (h *handlers) writeREST(w http.ResponseWriter, r *http.Request, m proto.Message) {
	b, err := wire.MarshalREST(m)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, json.RawMessage(b))
}

// listIndexes lists the database's composite indexes, of every collection group, each with its
// state (CREATING, READY, NEEDS_REPAIR).
func (h *handlers) listIndexes(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	res, err := h.svc.ListIndexes(r.Context(), t.call, &adminpb.ListIndexesRequest{Parent: t.group("-")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	list, err := restList(res.GetIndexes())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"indexes": list})
}

// createIndex creates a composite index of the body's collectionGroup: index is an Admin API
// Index ({"queryScope": …, "fields": [...]}). It answers the build's operation; the index is
// CREATING until the build ends.
func (h *handlers) createIndex(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	var in struct {
		CollectionGroup string          `json:"collectionGroup"`
		Index           json.RawMessage `json:"index"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	if in.CollectionGroup == "" || in.CollectionGroup == "-" {
		h.m.Fail(w, r, apierr.Invalid("collectionGroup", "The index's collection group is required."))
		return
	}
	ix := &adminpb.Index{}
	if len(in.Index) > 0 {
		if err := wire.UnmarshalREST(in.Index, ix); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	op, err := h.svc.CreateIndex(r.Context(), t.call, &adminpb.CreateIndexRequest{Parent: t.group(in.CollectionGroup), Index: ix})
	if err == nil {
		err = h.audit(r, t, "documents.index.create", in.CollectionGroup, map[string]any{"fields": len(ix.GetFields())})
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeREST(w, r, op)
}

func (h *handlers) deleteIndex(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	group, id := r.PathValue("group"), r.PathValue("id")
	if _, err := h.svc.DeleteIndex(r.Context(), t.call, &adminpb.DeleteIndexRequest{Name: t.group(group) + "/indexes/" + id}); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.audit(r, t, "documents.index.delete", group+"/"+id, nil); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, struct{}{})
}

// listFields lists the fields of every group the Admin API's filter selects.
func (h *handlers) listFields(w http.ResponseWriter, r *http.Request, filter string) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	res, err := h.svc.ListFields(r.Context(), t.call, &adminpb.ListFieldsRequest{Parent: t.group("-"), Filter: filter})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	list, err := restList(res.GetFields())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"fields": list})
}

// listOverrides lists the single-field exemptions: every field whose indexes are its own, the
// __default__ ones (the database's defaults) among them, as firebase-tools reads them.
func (h *handlers) listOverrides(w http.ResponseWriter, r *http.Request) {
	h.listFields(w, r, "indexConfig.usesAncestorConfig=false")
}

// listTTL lists the TTL policies, each with its state (CREATING until the field's documents are
// all scheduled, then ACTIVE).
func (h *handlers) listTTL(w http.ResponseWriter, r *http.Request) { h.listFields(w, r, "ttlConfig:*") }

// fieldTarget reads ?group= and ?field= (a field path, backquoted where it must be) of a
// field's route.
func fieldTarget(r *http.Request) (group, field string, err error) {
	q := r.URL.Query()
	group, field = q.Get("group"), q.Get("field")
	switch {
	case group == "" || group == "-":
		return "", "", apierr.Invalid("group", "The field's collection group is required.")
	case field == "":
		return "", "", apierr.Invalid("field", "The field's path is required.")
	}
	return group, field, nil
}

// updateField changes one part (mask, index_config or ttl_config) of a field's configuration;
// f without that part returns it to the ancestor's. It answers the change's operation.
func (h *handlers) updateField(w http.ResponseWriter, r *http.Request, f *adminpb.Field, mask, action string) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	group, field, err := fieldTarget(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	f.Name = t.group(group) + "/fields/" + field
	op, err := h.svc.UpdateField(r.Context(), t.call, &adminpb.UpdateFieldRequest{
		Field: f, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{mask}},
	})
	if err == nil {
		err = h.audit(r, t, action, group+"/"+field, nil)
	}
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeREST(w, r, op)
}

// setOverride sets a field's own indexes: the body is an Admin API Field's indexConfig part
// ({"indexConfig": {"indexes": [...]}}); no indexes exempts the field from indexing.
func (h *handlers) setOverride(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IndexConfig json.RawMessage `json:"indexConfig"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	f := &adminpb.Field{IndexConfig: &adminpb.Field_IndexConfig{}}
	if len(in.IndexConfig) > 0 {
		if err := wire.UnmarshalREST(in.IndexConfig, f.IndexConfig); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	h.updateField(w, r, f, "index_config", "documents.field.update")
}

// clearOverride returns a field's indexes to its ancestor's.
func (h *handlers) clearOverride(w http.ResponseWriter, r *http.Request) {
	h.updateField(w, r, &adminpb.Field{}, "index_config", "documents.field.clear")
}

// setTTL makes a field the TTL field of its collection group (firebase firestore:fields' ttl).
func (h *handlers) setTTL(w http.ResponseWriter, r *http.Request) {
	h.updateField(w, r, &adminpb.Field{TtlConfig: &adminpb.Field_TtlConfig{}}, "ttl_config", "documents.ttl.update")
}

// clearTTL removes a field's TTL policy.
func (h *handlers) clearTTL(w http.ResponseWriter, r *http.Request) {
	h.updateField(w, r, &adminpb.Field{}, "ttl_config", "documents.ttl.clear")
}

// listOperations lists the database's index and field operations, oldest first: their progress
// while an index or a policy builds.
func (h *handlers) listOperations(w http.ResponseWriter, r *http.Request) {
	t, err := h.target(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	res, err := h.svc.ListOperations(r.Context(), t.call, &longrunningpb.ListOperationsRequest{Name: t.database()})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	list, err := restList(res.GetOperations())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"operations": list})
}
