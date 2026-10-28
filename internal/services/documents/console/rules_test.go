package console

import (
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/platform/rules"
)

const openRules = "rules_version = '2';\nservice cloud.firestore {\n  match /databases/{database}/documents {\n    match /{document=**} {\n      allow read, write: if true;\n    }\n  }\n}\n"

// TestRules: unpublished, then a source that does not compile is refused with its issues'
// lines and columns (validate and publish alike, nothing saved), then one that compiles is
// published, read back and listed as current in the history; a Storage ruleset is not.
func TestRules(t *testing.T) {
	e := newEnv(t, true)
	if res := e.ok(owner, "GET", path(project, "/rules"), nil); res.json["published"] != false {
		t.Errorf("unpublished: %s", res.body)
	}
	bad := "rules_version = '2';\nservice cloud.firestore {\n  match /x {\n    allow read: if nope(;\n  }\n}\n"
	res := e.ok(owner, "POST", path(project, "/rules:validate"), map[string]any{"source": bad})
	issues, _ := res.json["issues"].([]any)
	if res.json["valid"] != false || len(issues) == 0 {
		t.Fatalf("validate: %s", res.body)
	}
	pos := issues[0].(map[string]any)["sourcePosition"].(map[string]any)
	if pos["line"] != float64(4) || pos["column"] == nil || pos["fileName"] != rulesFile {
		t.Errorf("issue position: %v", pos)
	}
	res = e.do(owner, "PUT", path(project, "/rules"), map[string]any{"source": bad})
	if res.status() != "INVALID_ARGUMENT" || !strings.Contains(string(res.body), `"sourcePosition"`) {
		t.Errorf("publish of a bad source: %d %s", res.code, res.body)
	}
	if res := e.ok(owner, "GET", path(project, "/rulesets"), nil); len(res.json["rulesets"].([]any)) != 0 {
		t.Errorf("a refused source was saved: %s", res.body)
	}
	if res := e.ok(owner, "POST", path(project, "/rules:validate"), map[string]any{"source": openRules}); res.json["valid"] != true {
		t.Errorf("validate a good source: %s", res.body)
	}

	// A Storage ruleset of the same project stays out of the history.
	if _, _, err := e.docs.Tenancy.CreateRuleset(t.Context(), e.docs.Owner, project, rules.Storage, []rules.File{{Name: "storage.rules",
		Content: "rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n    match /{all=**} {\n      allow read: if false;\n    }\n  }\n}\n"}}); err != nil {
		t.Fatal(err)
	}
	pub := e.ok(owner, "PUT", path(project, "/rules"), map[string]any{"source": openRules})
	name, _ := pub.json["rulesetName"].(string)
	if pub.json["published"] != true || !strings.HasPrefix(name, "projects/"+project+"/rulesets/") {
		t.Fatalf("publish: %s", pub.body)
	}
	if got := e.ok(owner, "GET", path(project, "/rules"), nil); got.json["source"] != openRules || got.json["rulesetName"] != name {
		t.Errorf("get after publish: %s", got.body)
	}
	hist := e.ok(owner, "GET", path(project, "/rulesets"), nil)
	list := hist.json["rulesets"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["name"] != name || list[0].(map[string]any)["current"] != true {
		t.Errorf("history: %s", hist.body)
	}
	id := name[strings.LastIndexByte(name, '/')+1:]
	if rs := e.ok(owner, "GET", path(project, "/rulesets/"+id), nil); rs.json["source"] != openRules {
		t.Errorf("ruleset: %s", rs.body)
	}
	// tenancy audited the publication.
	if _, ok := e.lastAudit("rules.release.publish"); !ok {
		t.Error("the publication was not audited")
	}
	if _, ok := e.lastAudit("rules.ruleset.create"); !ok {
		t.Error("the ruleset was not audited")
	}
}
