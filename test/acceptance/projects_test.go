package acceptance

import (
	"context"
	"regexp"
	"testing"
)

// AC-8: invalid or taken project ids are refused; ids are never reused.
func TestProjectIDs(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	for _, bad := range []string{"chat", "Chat-App", "1chat-app", "chat-app-", "chat_app"} {
		expect(t, c.do("POST", "/console/v1/projects", map[string]any{"projectId": bad, "displayName": "x"}), 400, "INVALID_ARGUMENT")
	}
	c.project("chat-app")
	expect(t, c.do("POST", "/console/v1/projects", map[string]any{"projectId": "chat-app", "displayName": "again"}), 409, "ALREADY_EXISTS")

	other := e.account("other@example.com")
	expect(t, other.do("POST", "/console/v1/projects", map[string]any{"projectId": "chat-app", "displayName": "mine"}), 409, "ALREADY_EXISTS")

	c.mustOK(c.do("POST", "/console/v1/projects/chat-app:delete", map[string]any{"confirmProjectId": "chat-app"}))
	expect(t, other.do("POST", "/console/v1/projects", map[string]any{"projectId": "chat-app", "displayName": "mine"}), 409, "ALREADY_EXISTS")

	check := c.mustOK(c.do("GET", "/console/v1/projects:checkId?projectId=chat-app", nil))
	if check.JSON["available"] != false || check.JSON["valid"] != true {
		t.Fatalf("checkId taken: %s", check.Body)
	}
	if c.mustOK(c.do("GET", "/console/v1/projects:checkId?projectId=Bad", nil)).JSON["valid"] != false {
		t.Fatal("checkId accepted an invalid id")
	}
	if s := c.mustOK(c.do("GET", "/console/v1/projects:suggestId?displayName=Chat%20App", nil)).str("projectId"); s == "chat-app" || !regexp.MustCompile(`^chat-app-[a-z0-9]{5}$`).MatchString(s) {
		t.Fatalf("suggestion for a taken id: %q", s)
	}
}

// AC-9: the project number has 12 digits and never changes.
func TestProjectNumberIsStable(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	p := c.project("chat-app")
	n := p.str("projectNumber")
	if !regexp.MustCompile(`^[1-9][0-9]{11}$`).MatchString(n) {
		t.Fatalf("project number %q", n)
	}
	c.mustOK(c.do("PATCH", "/console/v1/projects/chat-app", map[string]any{"displayName": "Renamed"}))
	byNumber := c.mustOK(c.do("GET", "/console/v1/projects/"+n, nil))
	assertEqual(t, "number after rename", byNumber.str("projectNumber"), n)
	assertEqual(t, "id by number", byNumber.str("projectId"), "chat-app")
	assertEqual(t, "renamed", byNumber.str("displayName"), "Renamed")
}

// AC-10, AC-11: deletion is soft for 30 days and restores the project with all its apps.
func TestProjectDeleteAndRestore(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	c.project("chat-app")
	web := c.app("chat-app", "WEB", "")
	android := c.app("chat-app", "ANDROID", "com.ahmad.chat")

	expect(t, c.do("POST", "/console/v1/projects/chat-app:delete", map[string]any{"confirmProjectId": "wrong"}), 400, "INVALID_ARGUMENT")
	del := c.mustOK(c.do("POST", "/console/v1/projects/chat-app:delete", map[string]any{"confirmProjectId": "chat-app"}))
	if del.str("state") != "DELETED" || del.str("expireTime") == "" {
		t.Fatalf("deleted project: %s", del.Body)
	}
	if len(c.mustOK(c.do("GET", "/console/v1/projects", nil)).list("projects")) != 0 {
		t.Fatal("deleted project still listed")
	}
	if len(c.mustOK(c.do("GET", "/console/v1/projects?showDeleted=true", nil)).list("projects")) != 1 {
		t.Fatal("showDeleted does not list it")
	}
	expect(t, c.do("GET", "/console/v1/projects/chat-app/apps", nil), 404, "NOT_FOUND")
	expect(t, c.do("GET", "/console/v1/projects/chat-app/apps/"+web.str("appId")+"/config", nil), 404, "NOT_FOUND")

	c.mustOK(c.do("POST", "/console/v1/projects/chat-app:restore", nil))
	apps := c.mustOK(c.do("GET", "/console/v1/projects/chat-app/apps", nil)).list("apps")
	if len(apps) != 2 {
		t.Fatalf("restored project has %d apps, want 2", len(apps))
	}
	c.mustOK(c.do("GET", "/console/v1/projects/chat-app/apps/"+android.str("appId")+"/config", nil))
	expect(t, c.do("POST", "/console/v1/projects/chat-app:restore", nil), 400, "FAILED_PRECONDITION")

	// Past the retention period the project cannot come back, and housekeeping empties it.
	c.mustOK(c.do("POST", "/console/v1/projects/chat-app:delete", map[string]any{"confirmProjectId": "chat-app"}))
	if _, err := e.Pool.Exec(context.Background(), "UPDATE control.projects SET expire_time = now() - interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	expect(t, c.do("POST", "/console/v1/projects/chat-app:restore", nil), 400, "FAILED_PRECONDITION")
	if n, err := e.App.Service.PurgeExpired(context.Background()); err != nil || n != 1 {
		t.Fatalf("PurgeExpired = %d, %v", n, err)
	}
	var apps2 int
	e.Pool.QueryRow(context.Background(), "SELECT count(*) FROM control.apps").Scan(&apps2)
	assertEqual(t, "apps after purge", apps2, 0)
	var projects int
	e.Pool.QueryRow(context.Background(), "SELECT count(*) FROM control.projects WHERE project_id = 'chat-app'").Scan(&projects)
	assertEqual(t, "tombstone rows", projects, 1)
}

// etag guards against lost updates.
func TestProjectETag(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	p := c.project("chat-app")
	c.mustOK(c.do("PATCH", "/console/v1/projects/chat-app", map[string]any{"displayName": "One", "etag": p.str("etag")}))
	expect(t, c.do("PATCH", "/console/v1/projects/chat-app", map[string]any{"displayName": "Two", "etag": p.str("etag")}), 409, "ABORTED")
	expect(t, c.do("PATCH", "/console/v1/projects/chat-app", map[string]any{"authDomain": "not a host"}), 400, "INVALID_ARGUMENT")
	r := c.mustOK(c.do("PATCH", "/console/v1/projects/chat-app", map[string]any{"authDomain": "Auth.Example.com"}))
	assertEqual(t, "authDomain", r.str("authDomain"), "auth.example.com")
}

// Administrative actions land in the project's audit log, newest first, without secrets.
func TestAuditLog(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	c.project("chat-app")
	c.app("chat-app", "WEB", "")
	c.mustOK(c.do("PATCH", "/console/v1/projects/chat-app", map[string]any{"displayName": "Chat"}))
	entries := c.mustOK(c.do("GET", "/console/v1/projects/chat-app/auditLog?pageSize=2", nil))
	list := entries.list("entries")
	if len(list) != 2 || list[0]["action"] != "project.update" || list[1]["action"] != "app.create" || list[0]["actorType"] != "account" {
		t.Fatalf("audit page: %s", entries.Body)
	}
	next := c.mustOK(c.do("GET", "/console/v1/projects/chat-app/auditLog?pageToken="+entries.str("nextPageToken"), nil)).list("entries")
	if len(next) != 1 || next[0]["action"] != "project.create" {
		t.Fatalf("audit second page: %v", next)
	}
}

func TestProjectListPagination(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	for _, id := range []string{"alpha-app", "bravo-app", "charlie-app"} {
		c.project(id)
	}
	first := c.mustOK(c.do("GET", "/console/v1/projects?pageSize=2", nil))
	second := c.mustOK(c.do("GET", "/console/v1/projects?pageSize=2&pageToken="+first.str("nextPageToken"), nil))
	if len(first.list("projects")) != 2 || len(second.list("projects")) != 1 || second.str("nextPageToken") != "" {
		t.Fatalf("pages: %s / %s", first.Body, second.Body)
	}
	assertEqual(t, "last", second.list("projects")[0]["projectId"], "charlie-app")
	expect(t, c.do("GET", "/console/v1/projects?pageToken=!!", nil), 400, "INVALID_ARGUMENT")
}
