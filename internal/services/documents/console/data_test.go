package console

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	pb "github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// fieldsJSON is a response document's fields, re-encoded for comparison.
func fieldsJSON(t *testing.T, doc map[string]any) string {
	t.Helper()
	b, err := json.Marshal(doc["fields"])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestDocuments: create (given id and auto-ID), get, set, update with a mask that deletes a
// field, delete; the values come back in Firestore's REST form, as written; every write is
// audited under the caller's account.
func TestDocuments(t *testing.T) {
	e := newEnv(t, true)
	values := map[string]any{
		"n":   map[string]any{"integerValue": "42"},
		"d":   map[string]any{"doubleValue": 1.5},
		"s":   map[string]any{"stringValue": "hi"},
		"b":   map[string]any{"booleanValue": true},
		"z":   map[string]any{"nullValue": nil},
		"t":   map[string]any{"timestampValue": "2026-10-28T10:00:00.123456Z"},
		"g":   map[string]any{"geoPointValue": map[string]any{"latitude": 24.7, "longitude": 46.7}},
		"r":   map[string]any{"referenceValue": "projects/" + project + "/databases/(default)/documents/users/u2"},
		"y":   map[string]any{"bytesValue": "AQI="},
		"a":   map[string]any{"arrayValue": map[string]any{"values": []any{map[string]any{"integerValue": "1"}}}},
		"m":   map[string]any{"mapValue": map[string]any{"fields": map[string]any{"k": map[string]any{"stringValue": "v"}}}},
		"del": map[string]any{"stringValue": "gone soon"},
	}
	created := e.ok(owner, "POST", path(project, "/documents", "path", "users"), map[string]any{"documentId": "u1", "fields": values})
	if created.json["path"] != "users/u1" || created.json["id"] != "u1" || created.json["missing"] != false ||
		created.json["name"] != "projects/"+project+"/databases/(default)/documents/users/u1" || created.json["createTime"] == nil {
		t.Fatalf("create: %s", created.body)
	}
	want, _ := json.Marshal(values)
	var wantFields map[string]any
	_ = json.Unmarshal(want, &wantFields)
	if got, w := fieldsJSON(t, created.json), fieldsJSON(t, map[string]any{"fields": wantFields}); got != w {
		t.Errorf("create's fields:\n got %s\nwant %s", got, w)
	}
	got := e.ok(owner, "GET", path(project, "/document", "path", "users/u1"), nil)
	if fieldsJSON(t, got.json) != fieldsJSON(t, created.json) {
		t.Errorf("get: %s", got.body)
	}
	if res := e.do(owner, "POST", path(project, "/documents", "path", "users"), map[string]any{"documentId": "u1"}); res.status() != "ALREADY_EXISTS" {
		t.Errorf("create of an existing document: %d %s", res.code, res.body)
	}

	auto := e.ok(owner, "POST", path(project, "/documents", "path", "users"), map[string]any{})
	if id, _ := auto.json["id"].(string); len(id) != 20 || auto.json["fields"] == nil {
		t.Errorf("auto-ID create: %s", auto.body)
	}

	// A mask deletes the field it names and the body leaves out; the others stay.
	upd := e.ok(owner, "PATCH", path(project, "/document", "path", "users/u1"), map[string]any{
		"fields": map[string]any{"s": map[string]any{"stringValue": "changed"}}, "updateMask": []string{"s", "del"}, "exists": true,
	})
	f := upd.json["fields"].(map[string]any)
	if _, ok := f["del"]; ok || f["s"].(map[string]any)["stringValue"] != "changed" || f["n"] == nil {
		t.Errorf("masked update: %s", upd.body)
	}
	// Without a mask the fields are replaced whole.
	set := e.ok(owner, "PATCH", path(project, "/document", "path", "users/u1"), map[string]any{
		"fields": map[string]any{"only": map[string]any{"integerValue": "1"}},
	})
	if got := fieldsJSON(t, set.json); got != `{"only":{"integerValue":"1"}}` {
		t.Errorf("set: %s", got)
	}
	// The Firebase console's edit: an updateTime precondition, so an edit over a stale read
	// fails and one over the current read goes through.
	if res := e.do(owner, "PATCH", path(project, "/document", "path", "users/u1"), map[string]any{
		"fields": map[string]any{}, "updateMask": []string{"only"}, "updateTime": upd.json["updateTime"],
	}); res.status() != "FAILED_PRECONDITION" {
		t.Errorf("edit over a stale read: %d %s", res.code, res.body)
	}
	e.ok(owner, "PATCH", path(project, "/document", "path", "users/u1"), map[string]any{
		"fields": map[string]any{"only": map[string]any{"integerValue": "2"}}, "updateMask": []string{"only"}, "updateTime": set.json["updateTime"],
	})
	// exists: true on a document that does not exist is NOT_FOUND, production's precondition.
	if res := e.do(owner, "PATCH", path(project, "/document", "path", "users/nope"), map[string]any{"exists": true}); res.status() != "NOT_FOUND" {
		t.Errorf("update with exists of a missing document: %d %s", res.code, res.body)
	}
	// A bad value is refused with the REST API's text.
	if res := e.do(owner, "PATCH", path(project, "/document", "path", "users/u1"), map[string]any{
		"fields": map[string]any{"x": map[string]any{"integerValue": "nope"}},
	}); res.status() != "INVALID_ARGUMENT" || res.message() == "" {
		t.Errorf("bad value: %d %s", res.code, res.body)
	}

	e.ok(owner, "DELETE", path(project, "/document", "path", "users/u1"), nil)
	if res := e.do(owner, "GET", path(project, "/document", "path", "users/u1"), nil); res.status() != "NOT_FOUND" {
		t.Errorf("get after delete: %d %s", res.code, res.body)
	}

	var actions []string
	for _, a := range e.audits() {
		if !strings.HasPrefix(a.action, "documents.") {
			continue
		}
		if a.actor != e.docs.Owner.AccountID.String() {
			t.Errorf("audit %s by %q", a.action, a.actor)
		}
		actions = append(actions, a.action+" "+a.target)
	}
	wantActions := []string{"documents.document.create users/u1", "documents.document.create users/" + auto.json["id"].(string),
		"documents.document.update users/u1", "documents.document.update users/u1", "documents.document.update users/u1", "documents.document.delete users/u1"}
	if !slices.Equal(actions, wantActions) {
		t.Errorf("audit:\n got %q\nwant %q", actions, wantActions)
	}
}

// TestAuditFailure: a write the audit log cannot record stands, and the caller is told.
func TestAuditFailure(t *testing.T) {
	e := newEnv(t, true)
	e.failAudit = true
	res := e.do(owner, "POST", path(project, "/documents", "path", "users"), map[string]any{"documentId": "u1"})
	if res.status() != "INTERNAL" {
		t.Errorf("create with a failing audit log: %d %s", res.code, res.body)
	}
	e.failAudit = false
	e.ok(owner, "GET", path(project, "/document", "path", "users/u1"), nil)
}

// TestListing: collections at the root and under a document, paged; a collection's documents in
// id order, paged, with a document that only has a subcollection flagged missing.
func TestListing(t *testing.T) {
	e := newEnv(t, true)
	for _, p := range []string{"users/b", "users/a", "users/c", "users/m/posts/p1", "users/a/posts/p1", "users/a/likes/l1", "rooms/r1"} {
		parent, id := splitCollection(p)
		e.ok(owner, "POST", path(project, "/documents", "path", parent), map[string]any{"documentId": id,
			"fields": map[string]any{"v": map[string]any{"stringValue": p}}})
	}
	res := e.ok(owner, "GET", path(project, "/collections"), nil)
	if fmt.Sprint(res.json["collectionIds"]) != "[rooms users]" {
		t.Errorf("root collections: %s", res.body)
	}
	res = e.ok(owner, "GET", path(project, "/collections", "path", "users/a", "pageSize", "1"), nil)
	tok, _ := res.json["nextPageToken"].(string)
	if fmt.Sprint(res.json["collectionIds"]) != "[likes]" || tok == "" {
		t.Fatalf("users/a's first page: %s", res.body)
	}
	res = e.ok(owner, "GET", path(project, "/collections", "path", "users/a", "pageSize", "1", "pageToken", tok), nil)
	if fmt.Sprint(res.json["collectionIds"]) != "[posts]" {
		t.Errorf("users/a's second page: %s", res.body)
	}
	// A missing document's subcollections are listed too.
	if res := e.ok(owner, "GET", path(project, "/collections", "path", "users/m"), nil); fmt.Sprint(res.json["collectionIds"]) != "[posts]" {
		t.Errorf("users/m's collections: %s", res.body)
	}

	var ids, missing []string
	token := ""
	for range 10 {
		q := []string{"path", "users", "pageSize", "2"}
		if token != "" {
			q = append(q, "pageToken", token)
		}
		res := e.ok(owner, "GET", path(project, "/documents", q...), nil)
		for _, d := range res.json["documents"].([]any) {
			doc := d.(map[string]any)
			ids = append(ids, doc["id"].(string))
			if doc["missing"] == true {
				missing = append(missing, doc["id"].(string))
				if doc["fields"] != nil || doc["createTime"] != nil {
					t.Errorf("missing document has data: %v", doc)
				}
			} else if doc["fields"] == nil {
				t.Errorf("document without fields: %v", doc)
			}
		}
		token, _ = res.json["nextPageToken"].(string)
		if token == "" {
			break
		}
	}
	if fmt.Sprint(ids) != "[a b c m]" || fmt.Sprint(missing) != "[m]" {
		t.Errorf("users: ids %v, missing %v", ids, missing)
	}
	// fields=false lists the names alone.
	res = e.ok(owner, "GET", path(project, "/documents", "path", "users", "fields", "false"), nil)
	if d := res.json["documents"].([]any)[0].(map[string]any); d["fields"].(map[string]any) == nil || len(d["fields"].(map[string]any)) != 0 {
		t.Errorf("fields=false: %s", res.body)
	}
	// A subcollection of a missing document lists as any other.
	res = e.ok(owner, "GET", path(project, "/documents", "path", "users/m/posts"), nil)
	if len(res.json["documents"].([]any)) != 1 {
		t.Errorf("users/m/posts: %s", res.body)
	}
}

// TestRecursiveDelete deletes a document and its descendants, then a collection and its
// descendants, firebase-tools' way; neighbours whose names share a prefix stay. A tree larger
// than one request's bound takes several, each reporting what it deleted.
func TestRecursiveDelete(t *testing.T) {
	e := newEnv(t, true)
	paths := []string{
		"users/u1", "users/u1/posts/p1", "users/u1/posts/p1/likes/l1", "users/u1/tags/t1",
		"users/u10", "users/u2", "users/m/posts/p1",
		"users2/x", "rooms/r1/users/u1",
	}
	for _, p := range paths {
		parent, id := splitCollection(p)
		e.ok(owner, "POST", path(project, "/documents", "path", parent), map[string]any{"documentId": id})
	}
	exists := func(p string) bool {
		res := e.do(owner, "GET", path(project, "/document", "path", p), nil)
		return res.code == http.StatusOK
	}
	res := e.ok(owner, "POST", path(project, "/documents:recursiveDelete"), map[string]any{"path": "users/u1"})
	if res.json["deleted"] != float64(4) || res.json["done"] != true {
		t.Errorf("delete users/u1 -r: %s", res.body)
	}
	for _, p := range paths {
		gone := p == "users/u1" || len(p) > len("users/u1/") && p[:9] == "users/u1/"
		if exists(p) == gone {
			t.Errorf("after users/u1 -r, %s exists %v", p, !gone)
		}
	}
	if a, ok := e.lastAudit("documents.recursive_delete"); !ok || a.target != "users/u1" || a.details["deleted"] != float64(4) {
		t.Errorf("audit: %+v", a)
	}

	// The collection, in requests of two documents at most.
	defer func(n int) { maxRecursiveDelete = n }(maxRecursiveDelete)
	maxRecursiveDelete = 2
	total, rounds := 0, 0
	for done := false; !done; rounds++ {
		if rounds > 5 {
			t.Fatal("recursive delete of users does not end")
		}
		res := e.ok(owner, "POST", path(project, "/documents:recursiveDelete"), map[string]any{"path": "users"})
		total += int(res.json["deleted"].(float64))
		done = res.json["done"] == true
	}
	if total != 3 || rounds != 2 {
		t.Errorf("delete users -r: %d documents in %d requests", total, rounds)
	}
	for _, p := range []string{"users/u10", "users/u2", "users/m/posts/p1"} {
		if exists(p) {
			t.Errorf("%s survived", p)
		}
	}
	for _, p := range []string{"users2/x", "rooms/r1/users/u1"} {
		if !exists(p) {
			t.Errorf("%s was deleted", p)
		}
	}
	// Nothing left: done at once, and nothing audited.
	before := len(e.audits())
	if res := e.ok(owner, "POST", path(project, "/documents:recursiveDelete"), map[string]any{"path": "users"}); res.json["deleted"] != float64(0) || res.json["done"] != true {
		t.Errorf("empty collection: %s", res.body)
	}
	if len(e.audits()) != before {
		t.Error("an empty delete was audited")
	}
	if res := e.do(owner, "POST", path(project, "/documents:recursiveDelete"), map[string]any{"path": ""}); res.status() != "INVALID_ARGUMENT" {
		t.Errorf("delete of the root: %d %s", res.code, res.body)
	}
}

// TestAdminWriteBypassesRules: with rules that deny everything, an end user is refused and the
// dashboard's writes and reads go through, as the Firebase console's do.
func TestAdminWriteBypassesRules(t *testing.T) {
	e := newEnv(t, true)
	e.docs.PublishFirestoreRules(t, "rules_version = '2';\nservice cloud.firestore {\n  match /databases/{database}/documents {\n    match /{document=**} {\n      allow read, write: if false;\n    }\n  }\n}\n")
	user := api.Call{Authorization: "Bearer " + apitest.UserToken(t, "alice", nil)}
	name := "projects/" + project + "/databases/(default)/documents/users/u1"
	if _, err := e.docs.Service.UpdateDocument(t.Context(), user, &pb.UpdateDocumentRequest{Document: &pb.Document{Name: name}}); err == nil {
		t.Fatal("the rules let an end user write")
	}
	e.ok(owner, "PATCH", path(project, "/document", "path", "users/u1"), map[string]any{"fields": map[string]any{"a": map[string]any{"integerValue": "1"}}})
	e.ok(owner, "GET", path(project, "/document", "path", "users/u1"), nil)
	e.ok(owner, "GET", path(project, "/documents", "path", "users"), nil)
	if res := e.ok(owner, "POST", path(project, "/documents:recursiveDelete"), map[string]any{"path": "users"}); res.json["deleted"] != float64(1) {
		t.Errorf("recursive delete under deny-all rules: %s", res.body)
	}
	// The console's caller is admitted as the project's owner, not as anyone: another account's
	// project is refused before the service layer is reached (TestAuthorization), and the
	// service layer admits the account alone to the projects it owns.
	other := api.Call{Caller: &authn.Caller{Role: authn.RoleAdmin, Principal: &authn.Principal{Kind: authn.KindAccount, AccountID: e.docs.Owner.AccountID}}}
	if _, err := e.docs.Service.GetDocument(t.Context(), other, &pb.GetDocumentRequest{Name: "projects/" + badrApp + "/databases/(default)/documents/users/u1"}); err == nil {
		t.Error("the service layer admitted an account to a project it does not own")
	}
}
