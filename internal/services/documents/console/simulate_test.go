package console

import (
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
)

const playRules = `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /users/{uid} {
      allow get: if request.auth != null && request.auth.uid == uid;
      allow create: if request.auth.token.email_verified == true && request.resource.data.name is string;
      allow update: if resource.data.owner == request.auth.uid && request.resource.data.owner == resource.data.owner;
      allow delete: if get(/databases/$(database)/documents/admins/$(request.auth.uid)).data.ok == true;
    }
    match /claims/{x} {
      allow get: if request.auth.token.firebase.sign_in_provider == 'password' && request.auth.token.aud == 'chatapp-8ae21'
        && request.auth.token.sub == 'ann' && request.auth.token.email == 'ann@example.com'
        && request.auth.token.email_verified == false && request.auth.token.name == 'Ann'
        && request.auth.token.phone_number == '+15555550100';
    }
  }
}
`

func str(s string) map[string]any { return map[string]any{"stringValue": s} }

// TestSimulateRules: the Rules Playground judges a get, create, update or delete as the rules
// would judge the user described: request.auth from the Playground's fields, the stored
// document as resource, request.resource the data given, get() reading the project's own
// documents. It answers allowed or denied with the statements evaluated, refuses a source that
// does not compile with its issues' positions, judges by the published rules when given no
// source, and writes nothing, nor counts its decisions in the usage.
func TestSimulateRules(t *testing.T) {
	e := newEnv(t, true)
	put := func(proj, p string, fields map[string]any) {
		t.Helper()
		e.ok(owner, "PATCH", path(proj, "/document", "path", p), map[string]any{"fields": fields})
	}
	put(project, "users/u1", map[string]any{"owner": str("alice")})
	put(project, "admins/alice", map[string]any{"ok": map[string]any{"booleanValue": true}})
	// carol is an admin of another project only: get() never reads it.
	put(apitest.OtherProject, "admins/carol", map[string]any{"ok": map[string]any{"booleanValue": true}})

	sim := func(body map[string]any) response {
		t.Helper()
		if _, ok := body["source"]; !ok {
			body["source"] = playRules
		}
		return e.ok(owner, "POST", path(project, "/rules:simulate"), body)
	}
	user := func(uid string) map[string]any { return map[string]any{"uid": uid} }
	for _, c := range []struct {
		name    string
		body    map[string]any
		allowed bool
	}{
		{"get unauthenticated", map[string]any{"method": "get", "path": "users/alice"}, false},
		{"get own", map[string]any{"method": "get", "path": "users/alice", "auth": user("alice")}, true},
		{"get another's", map[string]any{"method": "get", "path": "users/bob", "auth": user("alice")}, false},
		{"create verified", map[string]any{"method": "create", "path": "users/bob",
			"auth": map[string]any{"uid": "bob", "emailVerified": true}, "data": map[string]any{"name": str("Bob")}}, true},
		{"create unverified", map[string]any{"method": "create", "path": "users/bob",
			"auth": user("bob"), "data": map[string]any{"name": str("Bob")}}, false},
		{"create bad data", map[string]any{"method": "create", "path": "users/bob",
			"auth": map[string]any{"uid": "bob", "emailVerified": true}, "data": map[string]any{"name": map[string]any{"integerValue": "1"}}}, false},
		{"update by the stored owner", map[string]any{"method": "update", "path": "users/u1",
			"auth": user("alice"), "data": map[string]any{"owner": str("alice")}}, true},
		{"update changing the owner", map[string]any{"method": "update", "path": "users/u1",
			"auth": user("alice"), "data": map[string]any{"owner": str("bob")}}, false},
		{"update by another", map[string]any{"method": "update", "path": "users/u1",
			"auth": user("bob"), "data": map[string]any{"owner": str("alice")}}, false},
		{"delete by an admin, get()", map[string]any{"method": "delete", "path": "users/u1", "auth": user("alice")}, true},
		{"delete by another project's admin", map[string]any{"method": "delete", "path": "users/u1", "auth": user("carol")}, false},
		{"claims as the payload builds them", map[string]any{"method": "get", "path": "claims/x", "auth": map[string]any{
			"uid": "ann", "provider": "password", "email": "ann@example.com", "name": "Ann", "phoneNumber": "+15555550100"}}, true},
		{"claims with the default provider", map[string]any{"method": "get", "path": "claims/x", "auth": map[string]any{
			"uid": "ann", "email": "ann@example.com", "name": "Ann", "phoneNumber": "+15555550100"}}, false},
	} {
		res := sim(c.body)
		if res.json["allowed"] != c.allowed {
			t.Errorf("%s: %s", c.name, res.body)
		}
	}

	// The statements evaluated: the allowing one, with its line.
	res := sim(map[string]any{"method": "get", "path": "users/alice", "auth": user("alice")})
	st, _ := res.json["statements"].([]any)
	if len(st) != 1 {
		t.Fatalf("statements: %s", res.body)
	}
	if s := st[0].(map[string]any); s["line"] != float64(5) || s["outcome"] != "true" || s["column"] == nil {
		t.Errorf("the allowing statement: %v", s)
	}
	res = sim(map[string]any{"method": "get", "path": "users/alice"})
	if st, _ := res.json["statements"].([]any); len(st) != 1 || st[0].(map[string]any)["outcome"] == "true" {
		t.Errorf("the denying statement: %s", res.body)
	}

	// A source that does not compile: its issues, with their lines.
	bad := "rules_version = '2';\nservice cloud.firestore {\n  match /x {\n    allow read: if nope(;\n  }\n}\n"
	res = e.do(owner, "POST", path(project, "/rules:simulate"), map[string]any{"source": bad, "method": "get", "path": "users/a"})
	if res.status() != "INVALID_ARGUMENT" || !strings.Contains(string(res.body), `"line":4`) {
		t.Errorf("a source that does not compile: %d %s", res.code, res.body)
	}
	for _, body := range []map[string]any{
		{"method": "list", "path": "users/a"},
		{"method": "get", "path": "users"},
		{"method": "create", "path": "users/a", "data": map[string]any{"x": map[string]any{"nope": 1}}},
	} {
		body["source"] = playRules
		if res := e.do(owner, "POST", path(project, "/rules:simulate"), body); res.status() != "INVALID_ARGUMENT" {
			t.Errorf("%v: %d %s", body, res.code, res.body)
		}
	}

	// Without a source: the published rules, the locked default before any.
	if res := sim(map[string]any{"source": nil, "method": "get", "path": "users/alice", "auth": user("alice")}); res.json["allowed"] != false {
		t.Errorf("the locked default: %s", res.body)
	}
	e.ok(owner, "PUT", path(project, "/rules"), map[string]any{"source": openRules})
	if res := sim(map[string]any{"source": nil, "method": "delete", "path": "users/u1"}); res.json["allowed"] != true {
		t.Errorf("the published rules: %s", res.body)
	}

	// Nothing was written, and the decisions are not the project's usage.
	if res := e.do(owner, "GET", path(project, "/document", "path", "users/bob"), nil); res.status() != "NOT_FOUND" {
		t.Errorf("a simulated create was written: %d %s", res.code, res.body)
	}
	if res := e.ok(owner, "GET", path(project, "/document", "path", "users/u1"), nil); !strings.Contains(string(res.body), `"alice"`) {
		t.Errorf("a simulated update or delete was written: %s", res.body)
	}
	hours, err := e.docs.Usage.Hours(t.Context(), e.docs.Number, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hours {
		if h.RulesAllows+h.RulesDenies+h.RulesErrors != 0 || h.Writes != 2 {
			t.Errorf("usage after the simulations: %+v", h)
		}
	}
}
