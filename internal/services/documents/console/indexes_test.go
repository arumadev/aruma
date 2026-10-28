package console

import (
	"slices"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/core"
)

// TestIndexes: a composite index is created (its build's operation answered, the index listed
// with its state), refused when it repeats, and deleted; every change audited.
func TestIndexes(t *testing.T) {
	e := newEnv(t, true)
	if res := e.ok(owner, "GET", path(project, "/indexes"), nil); len(res.json["indexes"].([]any)) != 0 {
		t.Errorf("no index yet: %s", res.body)
	}
	body := map[string]any{"collectionGroup": "users", "index": map[string]any{
		"queryScope": "COLLECTION",
		"fields": []any{
			map[string]any{"fieldPath": "a", "order": "ASCENDING"},
			map[string]any{"fieldPath": "b", "order": "DESCENDING"},
		},
	}}
	op := e.ok(owner, "POST", path(project, "/indexes"), body)
	if name, _ := op.json["name"].(string); !strings.Contains(name, "/operations/") {
		t.Errorf("create: %s", op.body)
	}
	res := e.ok(owner, "GET", path(project, "/indexes"), nil)
	list := res.json["indexes"].([]any)
	if len(list) != 1 {
		t.Fatalf("indexes: %s", res.body)
	}
	ix := list[0].(map[string]any)
	name, _ := ix["name"].(string)
	prefix := "projects/" + project + "/databases/(default)/collectionGroups/users/indexes/"
	if !strings.HasPrefix(name, prefix) || (ix["state"] != "CREATING" && ix["state"] != "READY") || ix["queryScope"] != "COLLECTION" {
		t.Errorf("index: %v", ix)
	}
	if res := e.do(owner, "POST", path(project, "/indexes"), body); res.status() != "ALREADY_EXISTS" {
		t.Errorf("a repeated index: %d %s", res.code, res.body)
	}
	if res := e.do(owner, "POST", path(project, "/indexes"), map[string]any{"index": body["index"]}); res.status() != "INVALID_ARGUMENT" {
		t.Errorf("an index without a group: %d %s", res.code, res.body)
	}
	if res := e.ok(owner, "GET", path(project, "/operations"), nil); len(res.json["operations"].([]any)) == 0 {
		t.Errorf("operations: %s", res.body)
	}
	id := strings.TrimPrefix(name, prefix)
	e.ok(owner, "DELETE", path(project, "/indexes/users/"+id), nil)
	if res := e.ok(owner, "GET", path(project, "/indexes"), nil); len(res.json["indexes"].([]any)) != 0 {
		t.Errorf("after delete: %s", res.body)
	}
	if res := e.do(owner, "DELETE", path(project, "/indexes/users/"+id), nil); res.status() != "NOT_FOUND" {
		t.Errorf("delete again: %d %s", res.code, res.body)
	}
	if a, ok := e.lastAudit("documents.index.create"); !ok || a.target != "users" {
		t.Errorf("create's audit: %+v", a)
	}
	if a, ok := e.lastAudit("documents.index.delete"); !ok || a.target != "users/"+id {
		t.Errorf("delete's audit: %+v", a)
	}
}

// build runs the queued index and field changes, as the server's build job does.
func (e *env) build() {
	e.t.Helper()
	if err := e.docs.Store.BuildIndexes(e.t.Context(), core.IndexBuildOptions{}); err != nil {
		e.t.Fatal(err)
	}
}

// fieldNames lists the names of the fields a listing answered, below the database.
func fieldNames(res response) []string {
	var out []string
	for _, f := range res.json["fields"].([]any) {
		n := f.(map[string]any)["name"].(string)
		out = append(out, n[strings.Index(n, "/collectionGroups/")+len("/collectionGroups/"):])
	}
	return out
}

// TestFieldOverrides: a field exempted from indexing is listed among the overrides, and returns
// to its ancestor's configuration when cleared.
func TestFieldOverrides(t *testing.T) {
	e := newEnv(t, true)
	has := slices.Contains[[]string]
	e.ok(owner, "PUT", path(project, "/fields", "group", "users", "field", "bio"), map[string]any{"indexConfig": map[string]any{"indexes": []any{}}})
	e.build()
	res := e.ok(owner, "GET", path(project, "/fields"), nil)
	if !has(fieldNames(res), "users/fields/bio") {
		t.Errorf("overrides after the exemption: %s", res.body)
	}
	e.ok(owner, "PUT", path(project, "/fields", "group", "users", "field", "tags"), map[string]any{"indexConfig": map[string]any{"indexes": []any{
		map[string]any{"queryScope": "COLLECTION", "fields": []any{map[string]any{"fieldPath": "tags", "arrayConfig": "CONTAINS"}}},
	}}})
	e.ok(owner, "DELETE", path(project, "/fields", "group", "users", "field", "bio"), nil)
	e.build()
	res = e.ok(owner, "GET", path(project, "/fields"), nil)
	if names := fieldNames(res); has(names, "users/fields/bio") || !has(names, "users/fields/tags") {
		t.Errorf("overrides after clearing bio: %v", names)
	}
	if res := e.do(owner, "PUT", path(project, "/fields", "field", "bio"), map[string]any{}); res.status() != "INVALID_ARGUMENT" {
		t.Errorf("an override without a group: %d %s", res.code, res.body)
	}
	if a, ok := e.lastAudit("documents.field.clear"); !ok || a.target != "users/bio" {
		t.Errorf("audit: %+v", a)
	}
}

// TestTTL: a TTL policy is set (CREATING until the sweeper has scheduled the group, ACTIVE
// after), listed, and cleared.
func TestTTL(t *testing.T) {
	e := newEnv(t, true)
	e.ok(owner, "PUT", path(project, "/ttl", "group", "sessions", "field", "expireAt"), nil)
	e.build()
	res := e.ok(owner, "GET", path(project, "/ttl"), nil)
	fields := res.json["fields"].([]any)
	if len(fields) != 1 {
		t.Fatalf("ttl: %s", res.body)
	}
	f := fields[0].(map[string]any)
	state := f["ttlConfig"].(map[string]any)["state"]
	if !strings.HasSuffix(f["name"].(string), "/collectionGroups/sessions/fields/expireAt") || (state != "CREATING" && state != "ACTIVE") {
		t.Errorf("ttl policy: %v", f)
	}
	e.ok(owner, "DELETE", path(project, "/ttl", "group", "sessions", "field", "expireAt"), nil)
	e.build()
	if res := e.ok(owner, "GET", path(project, "/ttl"), nil); len(res.json["fields"].([]any)) != 0 {
		t.Errorf("ttl after clear: %s", res.body)
	}
	if _, ok := e.lastAudit("documents.ttl.update"); !ok {
		t.Error("not audited")
	}
}
