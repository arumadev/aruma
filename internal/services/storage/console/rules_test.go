package console

import (
	"net/http"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/services/storage/core"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

const (
	openRules = "rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n    match /{all=**} {\n      allow read, write: if true;\n    }\n  }\n}\n"
	badRules  = "rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n    allow read: if nope(;\n  }\n}\n"
)

// The editor reads the published rules, checks a source and publishes it through tenancy, as
// firebase deploy does: the release firebase.storage/<bucket> points at the new ruleset, /v0
// applies it at once, and tenancy audits both steps.
func TestRules(t *testing.T) {
	e := newEnv(t, true)
	got := e.do(amalSession, "GET", path("/rules"), nil)
	if got.code != http.StatusOK || got.json["published"] != false {
		t.Fatalf("before any: %d %s", got.code, got.body)
	}

	got = e.do(amalSession, "POST", path("/rules:validate"), map[string]any{"source": badRules})
	issues, _ := got.json["issues"].([]any)
	if got.code != http.StatusOK || got.json["valid"] != false || len(issues) == 0 {
		t.Fatalf("validate a bad source: %d %s", got.code, got.body)
	}
	first := issues[0].(map[string]any)
	pos, _ := first["sourcePosition"].(map[string]any)
	if first["severity"] != "ERROR" || pos["fileName"] != "storage.rules" || pos["line"] != float64(4) {
		t.Errorf("the issue: %v", first)
	}
	if got := e.do(amalSession, "POST", path("/rules:validate"), map[string]any{"source": openRules}); got.json["valid"] != true {
		t.Errorf("validate a good source: %s", got.body)
	}
	if n := len(e.audits()); n != 0 {
		t.Errorf("validating wrote %d audit lines", n)
	}

	// A source that does not compile is refused with its issues, and nothing is published.
	got = e.do(amalSession, "PUT", path("/rules"), map[string]any{"source": badRules})
	if got.code != http.StatusBadRequest || !strings.Contains(string(got.body), rulesIssuesType) ||
		!strings.Contains(string(got.body), `"line":4`) {
		t.Fatalf("publish a bad source: %d %s", got.code, got.body)
	}
	if got := e.do(amalSession, "GET", path("/rules"), nil); got.json["published"] != false {
		t.Errorf("after a refused publish: %s", got.body)
	}

	// Before: the locked default denies a user's read.
	user := core.Principal{Kind: core.PrincipalUser, UID: "u1", Token: map[string]any{"sub": "u1"}}
	e.insert("public/a.txt", "", "x")
	if _, err := e.svc.Stat(t.Context(), e.bucket, "public/a.txt", user, core.SurfaceFirebase, 0); err == nil {
		t.Fatal("the locked default allowed a read")
	}
	got = e.do(amalSession, "PUT", path("/rules"), map[string]any{"source": openRules})
	if got.code != http.StatusOK || got.json["published"] != true {
		t.Fatalf("publish: %d %s", got.code, got.body)
	}
	got = e.do(amalCLI, "GET", path("/rules"), nil)
	if got.json["source"] != openRules || !strings.HasPrefix(got.json["rulesetName"].(string), "projects/chat-app/rulesets/") {
		t.Errorf("read back: %s", got.body)
	}
	if _, err := e.svc.Stat(t.Context(), e.bucket, "public/a.txt", user, core.SurfaceFirebase, 0); err != nil {
		t.Errorf("the published rules do not apply: %v", err)
	}
	pub, ok := e.lastAudit("rules.release.publish")
	if !ok || pub.actor != amal || pub.target != "firebase.storage/chat-app.aruma.test" {
		t.Errorf("publish audit: %+v", pub)
	}
	if _, ok := e.lastAudit("rules.ruleset.create"); !ok {
		t.Error("no audit of the ruleset saved")
	}
}

// The stub stands for tenancy's rulesets until S5-A: it must keep tenancy's interface.
var _ Rules = (*tcore.Service)(nil)
