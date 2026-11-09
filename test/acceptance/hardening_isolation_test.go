package acceptance

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Hardening: project isolation, route by route. The existing isolation tests name the routes
// they cover; these walk the route tables the server registers, so a route added later is
// covered the day it is added, or the test fails for it.

// consoleBodies are bodies valid enough to pass a route's validation, so that the request reaches
// the ownership check instead of failing before it.
func consoleBody(method, pattern string) any {
	switch {
	case method == "POST" && strings.HasSuffix(pattern, "/apps"):
		return map[string]any{"platform": "WEB", "displayName": "intruder"}
	case method == "POST" && strings.HasSuffix(pattern, "/serviceAccounts"):
		return map[string]any{"accountId": "intruder"}
	case method == "POST" && strings.HasSuffix(pattern, "/sha"):
		return map[string]any{"shaHash": strings.Repeat("ab", 20)}
	case method == "PUT" && strings.HasSuffix(pattern, "/database/rules"):
		return map[string]any{"rules": `{"rules":{".read":true,".write":true}}`}
	case method == "PUT" && strings.HasSuffix(pattern, "/rules"):
		return map[string]any{"source": "rules_version='2';\nservice firebase.storage {\n match /b/{b}/o { match /{all=**} { allow read, write; } }\n}\n"}
	case method == "PATCH" && strings.HasSuffix(pattern, "/auth/config"):
		return map[string]any{"enumerationProtection": false}
	}
	return map[string]any{}
}

func fillRoute(pattern, project string) string {
	r := strings.NewReplacer("{project}", project, "{uid}", "alice", "{type}", "PASSWORD_RESET", "{language}", "en",
		"{providerId}", "google.com", "{ruleset}", "00000000-0000-7000-8000-000000000000", "{group}", "g", "{id}", "1",
		"{app}", "1:1:web:0", "{keyId}", "1", "{sha}", "x")
	return r.Replace(pattern)
}

// Owner B, by session cookie and by CLI token, reaches nothing of A through any console route:
// every route with a {project} answers NOT_FOUND for A's project, by id and by number, with the
// bytes it answers for a project that does not exist (so it is no oracle for either), and A's
// data is as it was.
func TestHardConsoleEveryProjectRouteIsolated(t *testing.T) {
	h := newHardEnv(t, nil)
	h.open(h.a, "alpha-secret")
	h.open(h.b, "bravo-secret")
	routes := 0
	snapshot := func() string {
		var b strings.Builder
		for _, path := range []string{"", "/apps", "/apiKeys", "/serviceAccounts", "/auth/users", "/auth/config", "/database/rules",
			"/database/data?path=/", "/storage/rules", "/storage/cors", "/documents/rules", "/storage/objects", "/documents/documents?path=c"} {
			r := h.a.owner.do("GET", "/console/v1/projects/"+h.a.id+path, nil)
			delete(r.JSON, "updateTime")
			fmt.Fprintf(&b, "%s %d %s\n", path, r.Status, r.Body)
		}
		return b.String()
	}
	before := snapshot()
	for _, rt := range h.App.Console.Routes() {
		if rt.Public || !strings.Contains(rt.Pattern, "{project}") {
			continue
		}
		routes++
		for _, ref := range []string{h.a.id, h.a.number} {
			for name, c := range map[string]*Client{"session": h.b.owner, "CLI token": h.b.cli} {
				path := fillRoute(rt.Pattern, ref) + "?path=/x&name=x"
				got := c.do(rt.Method, path, consoleBody(rt.Method, rt.Pattern))
				if got.Status != 404 || got.errStatus() != "NOT_FOUND" {
					t.Errorf("%s %s as B's %s: %d %s", rt.Method, path, name, got.Status, got.Body)
					continue
				}
				ghost := c.do(rt.Method, fillRoute(rt.Pattern, "ghost-app")+"?path=/x&name=x", consoleBody(rt.Method, rt.Pattern))
				if ghost.Status != got.Status || string(ghost.Body) != string(got.Body) {
					t.Errorf("%s %s as B's %s tells A's project from a missing one:\n%s\n%s", rt.Method, path, name, got.Body, ghost.Body)
				}
			}
		}
	}
	if routes < 90 {
		t.Fatalf("only %d project routes found", routes)
	}
	t.Logf("%d console routes with a {project}", routes)
	if after := snapshot(); after != before {
		t.Errorf("A's project changed:\n--- before\n%s--- after\n%s", before, after)
	}
}

// The Management API, the same way, with B's CLI token and B's service account token: all of its
// routes, those that name a project and those that name an app or an operation.
func TestHardManagementEveryProjectRouteIsolated(t *testing.T) {
	h := newHardEnv(t, nil)
	h.open(h.a, "alpha-secret")
	h.open(h.b, "bravo-secret")
	appA := h.a.owner.app(h.a.id, "WEB", "").str("appId")
	routes := 0
	for _, rt := range h.App.Mgmt.Routes() {
		if !strings.Contains(rt.Pattern, "{project}") && !strings.Contains(rt.Pattern, "{app}") && !strings.Contains(rt.Pattern, "{name...}") {
			continue
		}
		routes++
		for _, ref := range []string{h.a.id, h.a.number} {
			path := strings.NewReplacer("{project}", ref, "{app}", appA, "{name...}", "projects/"+ref+"/operations/x", "{sha}", "x",
				"{ruleset}", "00000000-0000-4000-8000-000000000000", "{release...}", "firebase.storage/"+h.a.bucket,
				"{service}", "firebasestorage.googleapis.com", "{instance}", "x").Replace(rt.Pattern)
			body := map[string]any{"displayName": "intruder", "bundleId": "com.intruder.app", "packageName": "com.intruder.app", "source": map[string]any{"files": []any{}}}
			for name, c := range map[string]*Client{"CLI token": h.b.cli, "service account": h.b.sa} {
				got := c.do(rt.Method, path, body)
				switch got.Status {
				case 404:
				case 400:
					// Rejected for its body before the project was looked at: it must also be
					// rejected for a project that does not exist, so nothing is told.
					ghost := c.do(rt.Method, strings.Replace(path, ref, "ghost-app", 1), body)
					if ghost.Status != 400 {
						t.Errorf("%s %s as B's %s: 400 for A's project but %d for a missing one", rt.Method, path, name, ghost.Status)
					}
				default:
					t.Errorf("%s %s as B's %s: %d %s", rt.Method, path, name, got.Status, got.Body)
				}
			}
		}
	}
	if routes < 20 {
		t.Fatalf("only %d routes found", routes)
	}
	// B's own listings carry nothing of A's.
	for _, c := range []*Client{h.b.cli, h.b.sa} {
		for _, path := range []string{"/v1beta1/projects", "/v1beta1/availableProjects", "/v1beta1/projects/" + h.b.id + ":searchApps"} {
			body := string(c.mustOK(c.do("GET", path, nil)).Body)
			for _, v := range []string{h.a.id, h.a.number, appA} {
				if strings.Contains(body, v) {
					t.Errorf("GET %s lists A's %q", path, v)
				}
			}
		}
	}
}

// A listing says nothing of a project the caller does not own, in any console or management
// list: not its id, its number, its bucket or its database.
func TestHardListsNeverNameAnotherProject(t *testing.T) {
	h := newHardEnv(t, nil)
	h.open(h.a, "alpha-secret")
	h.open(h.b, "bravo-secret")
	var leaks []string
	for _, rt := range h.App.Console.Routes() {
		if rt.Public || rt.Method != "GET" || strings.Contains(rt.Pattern, "{") && !strings.HasSuffix(rt.Pattern, "{project}") {
			continue
		}
		path := fillRoute(rt.Pattern, h.b.id)
		got := h.b.owner.do("GET", path, nil)
		for _, v := range []string{h.a.id, h.a.number, h.a.bucket, h.a.ns} {
			if strings.Contains(string(got.Body), v) {
				leaks = append(leaks, path+" contains "+v)
			}
		}
	}
	if len(leaks) > 0 {
		t.Errorf("leaks: %v", leaks)
	}
	var list struct {
		Projects []map[string]any `json:"projects"`
	}
	r := h.b.owner.mustOK(h.b.owner.do("GET", "/console/v1/projects?showDeleted=true&pageSize=100", nil))
	_ = json.Unmarshal(r.Body, &list)
	if len(list.Projects) != 1 {
		t.Errorf("B lists %d projects: %s", len(list.Projects), r.Body)
	}
}

// The Realtime Database's rules endpoint and its export, the one place a database's whole
// contents and policy move over REST, answer only the project's own admin credentials.
func TestHardRTDBRulesAndExportNeedTheProjectsAdmin(t *testing.T) {
	h := newHardEnv(t, nil)
	h.open(h.a, "alpha-secret")
	h.open(h.b, "bravo-secret")
	type cred struct{ name, bearer, query string }
	foreign := []cred{
		{"nothing", "", ""},
		{"A's service account", h.a.sat, ""},
		{"A's CLI token", h.a.pat, ""},
		{"A's service account in access_token", "", "&access_token=" + h.a.sat},
		{"A's user", "", "&auth=" + h.a.idToken},
		{"B's own user", "", "&auth=" + h.b.idToken},
		{"A's service account with an override of B's user", h.a.sat, "&auth_variable_override=%7B%22uid%22%3A%22alice%22%7D"},
		{"garbage", "garbage", ""},
	}
	for _, c := range foreign {
		for _, req := range []struct{ method, path, body string }{
			{"GET", "/.settings/rules.json?ns=" + h.b.ns, ""},
			{"PUT", "/.settings/rules.json?ns=" + h.b.ns, `{"rules":{".read":true,".write":true}}`},
			{"GET", "/.json?ns=" + h.b.ns + "&format=export", ""},
			{"PUT", "/.json?ns=" + h.b.ns, `{"pwned":true}`},
			{"DELETE", "/.json?ns=" + h.b.ns, ""},
		} {
			if c.name == "B's own user" && !strings.Contains(req.path, "/.settings/") {
				continue // the open rules let any signed-in user of B at B's data
			}
			r := restAs(t, h.Env, req.method, req.path+c.query, req.body, c.bearer, nil)
			if r.status == 200 {
				t.Errorf("%s: %s %s answered 200: %.100s", c.name, req.method, req.path, r.body)
			}
		}
	}
	if r := restAs(t, h.Env, "GET", "/.settings/rules.json?ns="+h.b.ns, "", h.b.sat, nil); r.status != 200 || strings.Contains(r.body, `".read":true`) {
		t.Errorf("B's rules after the attempts: %d %s", r.status, r.body)
	}
	if r := restAs(t, h.Env, "GET", "/secret.json?ns="+h.b.ns, "", h.b.sat, nil); r.body != `"bravo-secret"` {
		t.Errorf("B's data after the attempts: %d %s", r.status, r.body)
	}
	// The namespace of one project asked for through another's credential, spelled in every way
	// a URL can name it, serves nothing of B's.
	for _, ns := range []string{h.b.ns, strings.ToUpper(h.b.ns), h.b.ns + ".", h.b.ns + "%20", "%20" + h.b.ns, strings.ReplaceAll(h.b.ns, "-", "%2D"),
		h.b.ns + "&ns=" + h.a.ns, h.a.ns + "&ns=" + h.b.ns, h.b.ns + "%00", h.b.id, h.b.number + "-default-rtdb", h.b.ns + "/../" + h.a.ns} {
		r := restAs(t, h.Env, "GET", "/secret.json?ns="+ns, "", h.a.sat, nil)
		if strings.Contains(r.body, "bravo-secret") {
			t.Errorf("A's service account read B's data through ns=%q: %d %s", ns, r.status, r.body)
		}
		r = restAs(t, h.Env, "GET", "/secret.json?ns="+ns+"&auth="+h.a.idToken, "", "", nil)
		if strings.Contains(r.body, "bravo-secret") {
			t.Errorf("A's user read B's data through ns=%q: %d %s", ns, r.status, r.body)
		}
	}
}
