package rest_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	documents "github.com/arumadev/aruma/internal/services/documents/core"
)

// The Admin API over REST as firebase-tools calls it (ADR-025 §4.5): the bodies and query
// parameters it sends (lib/firestore/api.js of 13 and 15), and the answers in proto3 JSON.

const adminDB = "/v1/projects/" + apitest.Project + "/databases/(default)"

// adminDo sends an Admin request with the service account's JWT and decodes the answer.
func (h *harness) adminDo(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	code, _, b := h.do(t, method, path, body, h.env.JWT(t))
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s %s: %d %s: %v", method, path, code, b, err)
	}
	return code, m
}

func (h *harness) build(t *testing.T) {
	t.Helper()
	if err := h.env.Store.BuildIndexes(t.Context(), documents.IndexBuildOptions{}); err != nil {
		t.Fatal(err)
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantAdminError(t *testing.T, what string, code int, m map[string]any, wantCode int, status, msg string) {
	t.Helper()
	e, _ := m["error"].(map[string]any)
	if code != wantCode || e["status"] != status || msg != "" && e["message"] != msg {
		t.Errorf("%s: %d %v, want %d %s %q", what, code, m, wantCode, status, msg)
	}
}

// adminGaps are the f3-admin.json cases this server answers otherwise, with the reason; a listed
// case answered as production fails the test.
var adminGaps = map[string]string{
	"database.get_missing_collection_group": "Google's front end answers a path no API binds with its HTML 404 page; Aruma answers JSON NOT_FOUND",
}

// TestAdminCorpusReplay replays every case of f3-admin.json: an owner's reads of production's
// Admin API on a project with no composite index and no field override, as a fresh database is
// here. The project's own facts (its database's uid and location) and the values the recorder
// replaced by placeholders (times, etag) are taken from the recording.
func TestAdminCorpusReplay(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "oracle", "documents", "corpus", "f3-admin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f corpusFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	compared, gaps := 0, 0
	for _, c := range f.Cases {
		code, _, got := h.do(t, c.Request.Method, "/v1/"+strings.ReplaceAll(c.Request.Path, "{project}", apitest.Project), "", h.env.JWT(t))
		compared++
		got = fromRecording(t, got, c.Response.Body)
		ok := code == c.Response.HTTP && sameJSON(t, got, c.Response.Body)
		if reason, known := adminGaps[c.ID]; known {
			gaps++
			if ok {
				t.Errorf("%s: answered as production, but listed as a gap (%s)", c.ID, reason)
			}
			continue
		}
		if !ok {
			t.Errorf("%s: GET %s\n got %d %s\nwant %d %s", c.ID, c.Request.Path, code, got, c.Response.HTTP, c.Response.Body)
		}
	}
	if compared < 30 || gaps != len(adminGaps) {
		t.Errorf("compared %d cases, %d gaps", compared, gaps)
	}
}

// fromRecording puts into got, at the same places, what the recording holds as placeholders
// (<…>) and the recorded project's own uid and locationId.
func fromRecording(t *testing.T, got, want []byte) []byte {
	t.Helper()
	var g, w any
	if json.Unmarshal(got, &g) != nil || json.Unmarshal(want, &w) != nil {
		return got
	}
	var walk func(g, w any)
	walk = func(g, w any) {
		switch x := g.(type) {
		case map[string]any:
			y, _ := w.(map[string]any)
			for k, e := range x {
				ws, isStr := y[k].(string)
				switch {
				case isStr && strings.HasPrefix(ws, "<"), isStr && (k == "uid" || k == "locationId"):
					x[k] = ws
				default:
					walk(e, y[k])
				}
			}
		case []any:
			y, _ := w.([]any)
			for i, e := range x {
				if i < len(y) {
					walk(e, y[i])
				}
			}
		}
	}
	walk(g, w)
	return []byte(jsonOf(t, g))
}

func TestAdminDatabases(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	code, d := h.adminDo(t, http.MethodGet, adminDB, "")
	if code != 200 || d["name"] != "projects/"+apitest.Project+"/databases/(default)" || d["type"] != "FIRESTORE_NATIVE" ||
		d["databaseEdition"] != "STANDARD" || d["concurrencyMode"] != "PESSIMISTIC" || d["versionRetentionPeriod"] != "3600s" ||
		d["deleteProtectionState"] != "DELETE_PROTECTION_DISABLED" || d["uid"] == nil || d["earliestVersionTime"] == nil {
		t.Errorf("GetDatabase: %d %v", code, d)
	}
	code, l := h.adminDo(t, http.MethodGet, "/v1/projects/"+apitest.Project+"/databases", "")
	if dbs, _ := l["databases"].([]any); code != 200 || len(dbs) != 1 || dbs[0].(map[string]any)["uid"] != d["uid"] {
		t.Errorf("ListDatabases: %d %v", code, l)
	}
	// f0-missing-database.json admin.getDatabase, byte for byte.
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "oracle", "documents", "corpus", "f0-missing-database.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f corpusFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	replayed := 0
	for _, c := range f.Cases {
		if !strings.HasPrefix(c.ID, "admin.") {
			continue
		}
		code, _, got := h.do(t, c.Request.Method, "/v1/"+strings.ReplaceAll(c.Request.Path, "{project}", apitest.Project), "", h.env.JWT(t))
		if code != c.Response.HTTP || !sameJSON(t, got, c.Response.Body) {
			t.Errorf("%s: %d %s\nwant %d %s", c.ID, code, got, c.Response.HTTP, c.Response.Body)
		}
		replayed++
	}
	if replayed != 1 {
		t.Errorf("%d admin cases replayed", replayed)
	}
	// The missing database is the same answer under it.
	code, m := h.adminDo(t, http.MethodGet, "/v1/projects/"+apitest.Project+"/databases/aruma-ref-nonexistent/collectionGroups/-/indexes", "")
	wantAdminError(t, "indexes of a missing database", code, m, 404, "NOT_FOUND",
		"Project '"+apitest.Project+"' or database 'aruma-ref-nonexistent' does not exist.")
	// Creating a database (firebase deploy after a 404) and the methods not served.
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v1/projects/" + apitest.Project + "/databases?databaseId=second"},
		{http.MethodPatch, adminDB},
		{http.MethodDelete, adminDB},
		{http.MethodPost, adminDB + ":exportDocuments"},
		{http.MethodPost, adminDB + "/operations/x:cancel"},
		{http.MethodGet, adminDB + "/backups"},
	} {
		code, m := h.adminDo(t, c.method, c.path, "")
		wantAdminError(t, c.method+" "+c.path, code, m, 501, "UNIMPLEMENTED", "")
	}
}

// longName is a document name 6127 bytes long below documents/, whose entries are too large
// (f0-errors.json rest.name_bytes_6127).
func longName() string {
	var b strings.Builder
	for i := range 4 {
		if i > 0 {
			b.WriteString("/")
		}
		b.WriteString("c/" + strings.Repeat(string(rune('a'+i)), 1500))
	}
	b.WriteString("/c/")
	b.WriteString(strings.Repeat("e", 6127-b.Len()))
	return b.String()
}

// core's ErrIndexEntryTooLarge reaches REST with the ErrorInfo production attaches.
func TestIndexEntryTooLargeDetails(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	code, _, b := h.do(t, http.MethodPost, adminDB+"/documents:commit",
		`{"writes":[{"update":{"name":"`+root+"/"+longName()+`","fields":{"a":{"integerValue":"1"}}}}]}`, h.env.JWT(t))
	want := `{"error":{"code":400,"message":"Index entry is too large.","status":"INVALID_ARGUMENT","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo",` +
		`"reason":"INDEX_ENTRY_TOO_LARGE","domain":"firestore.googleapis.com"}]}}`
	if code != 400 || !sameJSON(t, b, []byte(want)) {
		t.Errorf("%d %s\nwant %s", code, b, want)
	}
}

// The Admin API is an admin's: a service account, the owner's aruma_pat_, the dev owner.
func TestAdminCallers(t *testing.T) {
	h := newHarness(t, apitest.Options{DevOwnerToken: true, DevOpenRules: true})
	path := adminDB + "/collectionGroups/-/indexes"
	for _, tok := range []string{h.env.JWT(t), h.env.OwnerPAT, "owner"} {
		if code, _, b := h.do(t, http.MethodGet, path, "", tok); code != 200 || string(b) != "{}" {
			t.Errorf("admin %.12q: %d %s", tok, code, b)
		}
	}
	// dev_open_rules opens the data, not the Admin API.
	code, _, b := h.do(t, http.MethodGet, path, "", "")
	if want := `{"error":{"code":401,"message":"Request is missing required authentication credential. Expected OAuth 2 access token, ` +
		`login cookie or other valid authentication credential. See https://developers.google.com/identity/sign-in/web/devconsole-project.",` +
		`"status":"UNAUTHENTICATED"}}`; code != 401 || string(b) != want {
		t.Errorf("anonymous: %d %s", code, b)
	}
	code, _, b = h.do(t, http.MethodGet, path, "", apitest.IDToken(t))
	if want := `{"error":{"code":403,"message":"The caller does not have permission","status":"PERMISSION_DENIED"}}`; code != 403 || string(b) != want {
		t.Errorf("end user: %d %s", code, b)
	}
	// Another project's indexes, with this project's service account.
	code, _, b = h.do(t, http.MethodGet, "/v1/projects/"+apitest.OtherProject+"/databases/(default)/collectionGroups/-/indexes", "", h.env.JWT(t))
	if code != 403 || !strings.Contains(string(b), "PERMISSION_DENIED") {
		t.Errorf("other project: %d %s", code, b)
	}
}

func TestAdminIndexes(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	group := adminDB + "/collectionGroups/"
	// firebase-tools' createIndex body: fields and queryScope.
	code, op := h.adminDo(t, http.MethodPost, group+"cities/indexes",
		`{"fields":[{"fieldPath":"country","order":"ASCENDING"},{"fieldPath":"population","order":"DESCENDING"}],"queryScope":"COLLECTION"}`)
	md, _ := op["metadata"].(map[string]any)
	if code != 200 || op["done"] != nil || md["@type"] != "type.googleapis.com/google.firestore.admin.v1.IndexOperationMetadata" ||
		md["state"] != "PROCESSING" || !strings.HasPrefix(op["name"].(string), "projects/"+apitest.Project+"/databases/(default)/operations/") {
		t.Fatalf("CreateIndex: %d %v", code, op)
	}
	name := md["index"].(string)
	want := `{"density":"SPARSE_ALL","fields":[{"fieldPath":"country","order":"ASCENDING"},{"fieldPath":"population","order":"DESCENDING"},` +
		`{"fieldPath":"__name__","order":"DESCENDING"}],"name":"` + name + `","queryScope":"COLLECTION","state":"CREATING"}`
	if code, ix := h.adminDo(t, http.MethodGet, "/v1/"+name, ""); code != 200 || jsonOf(t, ix) != want {
		t.Errorf("GetIndex: %d %s\nwant %s", code, jsonOf(t, ix), want)
	}
	// A collection-group array-contains index, and a vector index: __name__ before the vector.
	if code, op := h.adminDo(t, http.MethodPost, group+"tags/indexes",
		`{"fields":[{"fieldPath":"labels","arrayConfig":"CONTAINS"},{"fieldPath":"rank","order":"DESCENDING"}],"queryScope":"COLLECTION_GROUP"}`); code != 200 {
		t.Fatalf("collection-group index: %d %v", code, op)
	}
	code, vop := h.adminDo(t, http.MethodPost, group+"docs/indexes",
		`{"fields":[{"fieldPath":"kind","order":"DESCENDING"},{"fieldPath":"embedding","vectorConfig":{"dimension":3,"flat":{}}}],"queryScope":"COLLECTION"}`)
	if code != 200 || vop["done"] != true {
		t.Fatalf("vector index: %d %v", code, vop)
	}
	vix := vop["response"].(map[string]any)
	if got := jsonOf(t, vix["fields"]); got != `[{"fieldPath":"kind","order":"DESCENDING"},{"fieldPath":"__name__","order":"DESCENDING"},`+
		`{"fieldPath":"embedding","vectorConfig":{"dimension":3,"flat":{}}}]` || vix["state"] != "READY" {
		t.Errorf("vector index: %v", vix)
	}
	// The same index again.
	code, m := h.adminDo(t, http.MethodPost, group+"cities/indexes",
		`{"fields":[{"fieldPath":"country","order":"ASCENDING"},{"fieldPath":"population","order":"DESCENDING"}],"queryScope":"COLLECTION"}`)
	wantAdminError(t, "the same index", code, m, 409, "ALREADY_EXISTS", "index already exists")
	// A malformed id, whatever the group (f3-admin.json indexes.get_missing, get_dash_group).
	code, m = h.adminDo(t, http.MethodGet, group+"-/indexes/nonexistent", "")
	wantAdminError(t, "a malformed id", code, m, 400, "INVALID_ARGUMENT", `Invalid index resource id "nonexistent".`)

	h.build(t)
	code, l := h.adminDo(t, http.MethodGet, group+"-/indexes", "")
	ixs, _ := l["indexes"].([]any)
	if code != 200 || len(ixs) != 3 {
		t.Fatalf("ListIndexes -: %d %v", code, l)
	}
	for _, ix := range ixs {
		if ix.(map[string]any)["state"] != "READY" {
			t.Errorf("after the build: %v", ix)
		}
	}
	if code, l := h.adminDo(t, http.MethodGet, group+"tags/indexes", ""); code != 200 || len(l["indexes"].([]any)) != 1 {
		t.Errorf("ListIndexes tags: %d %v", code, l)
	}
	// One page alone; operations take a page size.
	code, m = h.adminDo(t, http.MethodGet, group+"-/indexes?pageSize=2", "")
	wantAdminError(t, "a page size", code, m, 400, "INVALID_ARGUMENT", "Invalid page size. Only 0 is supported.")
	code, m = h.adminDo(t, http.MethodGet, group+"-/indexes?pageToken=abc", "")
	wantAdminError(t, "a page token", code, m, 400, "INVALID_ARGUMENT", "Invalid page token.")
	code, p1 := h.adminDo(t, http.MethodGet, adminDB+"/operations?pageSize=2", "")
	token, _ := p1["nextPageToken"].(string)
	if code != 200 || len(p1["operations"].([]any)) != 2 || token == "" {
		t.Fatalf("operations page 1: %d %v", code, p1)
	}
	if code, p2 := h.adminDo(t, http.MethodGet, adminDB+"/operations?pageSize=2&pageToken="+token, ""); code != 200 ||
		len(p2["operations"].([]any)) != 1 || p2["nextPageToken"] != nil {
		t.Errorf("operations page 2: %d %v", code, p2)
	}

	// The operation, done once the index is READY.
	code, got := h.adminDo(t, http.MethodGet, "/v1/"+op["name"].(string), "")
	res, _ := got["response"].(map[string]any)
	if code != 200 || got["done"] != true || res["@type"] != "type.googleapis.com/google.firestore.admin.v1.Index" || res["state"] != "READY" {
		t.Errorf("GetOperation: %d %v", code, got)
	}
	if code, ops := h.adminDo(t, http.MethodGet, adminDB+"/operations", ""); code != 200 || len(ops["operations"].([]any)) != 3 {
		t.Errorf("ListOperations: %d %v", code, ops)
	}
	code, m = h.adminDo(t, http.MethodGet, adminDB+"/operations/nope", "")
	wantAdminError(t, "a missing operation", code, m, 404, "NOT_FOUND", "Operation does not exist")
	code, m = h.adminDo(t, http.MethodGet, adminDB+"/operations/AAAAAAAAAAAAAAAAAAAAAA", "")
	wantAdminError(t, "a missing operation of this server's form", code, m, 404, "NOT_FOUND", "Requested entity was not found.")

	// firebase deploy --force: DELETE /{index.name}.
	if code, _, b := h.do(t, http.MethodDelete, "/v1/"+name, "", h.env.JWT(t)); code != 200 || string(b) != "{}" {
		t.Errorf("DeleteIndex: %d %s", code, b)
	}
	code, m = h.adminDo(t, http.MethodGet, "/v1/"+name, "")
	wantAdminError(t, "a deleted index", code, m, 404, "NOT_FOUND", "index not found.")
	code, m = h.adminDo(t, http.MethodDelete, "/v1/"+name, "")
	wantAdminError(t, "deleted twice", code, m, 404, "NOT_FOUND", "index not found.")

	// Refusals.
	for _, c := range []struct{ what, method, path, body string }{
		{"create under -", http.MethodPost, group + "-/indexes", `{"fields":[{"fieldPath":"a","order":"ASCENDING"},{"fieldPath":"b","order":"ASCENDING"}],"queryScope":"COLLECTION"}`},
		{"one field", http.MethodPost, group + "c/indexes", `{"fields":[{"fieldPath":"a","order":"ASCENDING"}],"queryScope":"COLLECTION"}`},
		{"an unknown member", http.MethodPost, group + "c/indexes", `{"fields":[],"queryScope":"COLLECTION","colour":1}`},
		{"a DENSE index", http.MethodPost, group + "c/indexes", `{"fields":[{"fieldPath":"a","order":"ASCENDING"},{"fieldPath":"b","order":"ASCENDING"}],"queryScope":"COLLECTION","density":"DENSE"}`},
		{"a filter", http.MethodGet, group + "-/indexes?filter=x", ""},
		{"a negative page size", http.MethodGet, adminDB + "/operations?pageSize=-1", ""},
		{"an operations filter", http.MethodGet, adminDB + "/operations?filter=done", ""},
	} {
		code, m := h.adminDo(t, c.method, c.path, c.body)
		wantAdminError(t, c.what, code, m, 400, "INVALID_ARGUMENT", "")
	}
	// The density an exported firestore.indexes.json carries back is accepted.
	if code, m := h.adminDo(t, http.MethodPost, group+"c/indexes",
		`{"fields":[{"fieldPath":"a","order":"ASCENDING"},{"fieldPath":"b","order":"ASCENDING"}],"queryScope":"COLLECTION","density":"SPARSE_ALL","apiScope":"ANY_API"}`); code != 200 {
		t.Errorf("SPARSE_ALL: %d %v", code, m)
	}
}

func TestAdminFields(t *testing.T) {
	h := newHarness(t, apitest.Options{})
	fields := adminDB + "/collectionGroups/"
	// firebase-tools' patchField: an exemption without ttl (updateMask=indexConfig), and an
	// override with a TTL policy (no mask).
	code, op := h.adminDo(t, http.MethodPatch, fields+"posts/fields/body?updateMask=indexConfig", `{"indexConfig":{"indexes":[]}}`)
	if md, _ := op["metadata"].(map[string]any); code != 200 || op["done"] != nil ||
		md["@type"] != "type.googleapis.com/google.firestore.admin.v1.FieldOperationMetadata" {
		t.Fatalf("UpdateField: %d %v", code, op)
	}
	if code, op := h.adminDo(t, http.MethodPatch, fields+"sessions/fields/expireAt",
		`{"indexConfig":{"indexes":[{"queryScope":"COLLECTION","fields":[{"fieldPath":"expireAt","order":"ASCENDING"}]},`+
			`{"queryScope":"COLLECTION_GROUP","fields":[{"fieldPath":"expireAt","order":"DESCENDING"}]}]},"ttlConfig":{}}`); code != 200 {
		t.Fatalf("UpdateField with ttl: %d %v", code, op)
	}
	// A quoted path, percent-encoded as a URL carries it.
	if code, op := h.adminDo(t, http.MethodPatch, fields+"posts/fields/"+url.PathEscape("`a-b`")+"?updateMask=indexConfig",
		`{"indexConfig":{"indexes":[{"queryScope":"COLLECTION","fields":[{"fieldPath":"`+"`a-b`"+`","arrayConfig":"CONTAINS"}]}]}}`); code != 200 {
		t.Fatalf("UpdateField of a quoted path: %d %v", code, op)
	}
	h.build(t)

	list := fields + "-/fields?filter=" + url.QueryEscape("indexConfig.usesAncestorConfig=false OR ttlConfig:*")
	code, l := h.adminDo(t, http.MethodGet, list, "")
	var names []string
	for _, f := range l["fields"].([]any) {
		names = append(names, f.(map[string]any)["name"].(string))
	}
	p := "projects/" + apitest.Project + "/databases/(default)/collectionGroups/"
	if code != 200 || strings.Join(names, " ") != p+"__default__/fields/* "+p+"posts/fields/`a-b` "+p+"posts/fields/body "+p+"sessions/fields/expireAt" {
		t.Fatalf("ListFields: %d %v", code, names)
	}
	exp := l["fields"].([]any)[3].(map[string]any)
	if ttl, _ := exp["ttlConfig"].(map[string]any); ttl["state"] == nil || len(exp["indexConfig"].(map[string]any)["indexes"].([]any)) != 2 {
		t.Errorf("the TTL field: %v", exp)
	}
	// gcloud's filter, and the TTL term alone.
	if code, l := h.adminDo(t, http.MethodGet, fields+"posts/fields?filter="+url.QueryEscape("indexConfig.usesAncestorConfig:false"), ""); code != 200 || len(l["fields"].([]any)) != 2 {
		t.Errorf("gcloud's filter: %d %v", code, l)
	}
	if code, l := h.adminDo(t, http.MethodGet, fields+"-/fields?filter="+url.QueryEscape("ttlConfig:*"), ""); code != 200 || len(l["fields"].([]any)) != 1 {
		t.Errorf("ttlConfig:*: %d %v", code, l)
	}
	code, m := h.adminDo(t, http.MethodGet, fields+"-/fields?filter="+url.QueryEscape("indexConfig.usesAncestorConfig=true"), "")
	wantAdminError(t, "another filter", code, m, 400, "INVALID_ARGUMENT",
		"ListFieldsRequest.filter must include at least fields that do not use their ancestor configuration, or fields with TTLs.")

	// One field: its own, inherited, and the database's default.
	if code, f := h.adminDo(t, http.MethodGet, fields+"posts/fields/body", ""); code != 200 ||
		f["indexConfig"].(map[string]any)["usesAncestorConfig"] != nil || f["indexConfig"].(map[string]any)["indexes"] != nil {
		t.Errorf("GetField body: %d %v", code, f)
	}
	if code, f := h.adminDo(t, http.MethodGet, fields+"posts/fields/title", ""); code != 200 ||
		f["indexConfig"].(map[string]any)["usesAncestorConfig"] != true ||
		f["indexConfig"].(map[string]any)["ancestorField"] != p+"__default__/fields/*" {
		t.Errorf("GetField title: %d %v", code, f)
	}
	if code, f := h.adminDo(t, http.MethodGet, fields+"__default__/fields/*", ""); code != 200 || f["name"] != p+"__default__/fields/*" {
		t.Errorf("GetField default: %d %v", code, f)
	}
	code, m = h.adminDo(t, http.MethodGet, fields+"posts/fields/a b", "")
	wantAdminError(t, "a path that does not parse", code, m, 400, "INVALID_ARGUMENT", "")

	// firebase-tools' deleteField: PATCH /{field.name} with {}.
	if code, op := h.adminDo(t, http.MethodPatch, "/v1/"+p+"posts/fields/body", `{}`); code != 200 {
		t.Fatalf("revert: %d %v", code, op)
	}
	h.build(t)
	if code, f := h.adminDo(t, http.MethodGet, fields+"posts/fields/body", ""); code != 200 || f["indexConfig"].(map[string]any)["usesAncestorConfig"] != true {
		t.Errorf("after the revert: %d %v", code, f)
	}
	if code, l := h.adminDo(t, http.MethodGet, list, ""); code != 200 || len(l["fields"].([]any)) != 3 {
		t.Errorf("ListFields after the revert: %d %v", code, l)
	}
	code, m = h.adminDo(t, http.MethodPatch, fields+"posts/fields/x?updateMask=colour", `{}`)
	wantAdminError(t, "an unknown mask", code, m, 400, "INVALID_ARGUMENT", "")
}
