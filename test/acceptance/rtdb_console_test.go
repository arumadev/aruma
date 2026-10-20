package acceptance

import (
	"net/url"
	"strings"
	"testing"
)

// The console's Realtime Database routes (docs/api/console.md): the data browser and the rules
// editor, on a real server and Postgres.

func consoleDB(project string) string { return "/console/v1/projects/" + project + "/database" }

func consoleData(project, path string, query ...string) string {
	q := url.Values{"path": {path}}
	for i := 0; i+1 < len(query); i += 2 {
		q.Set(query[i], query[i+1])
	}
	return consoleDB(project) + "/data?" + q.Encode()
}

func TestRTDBConsoleData(t *testing.T) {
	e := rtdbEnv(t, false, map[string]string{"ARUMA_RTDB_DOMAIN": "rtdb.aruma.test"})
	c := e.owner()
	ns := c.projectNS("chat-app")

	info := c.mustOK(c.do("GET", consoleDB("chat-app"), nil))
	assertEqual(t, "databaseURL", info.str("databaseURL"), "https://chat-app-default-rtdb.rtdb.aruma.test")
	assertEqual(t, "enabled", info.JSON["enabled"], true)
	assertEqual(t, "devOpenRules", info.JSON["devOpenRules"], true)

	// A client listens while the console writes: each write reaches it, as an SDK's would.
	ws := e.dial(ns, dialOpts{})
	ws.call("q", map[string]any{"p": "/rooms", "h": ""}).mustOK(t)
	expect(t, c.do("PUT", consoleData("chat-app", "/rooms/r1"), map[string]any{"value": map[string]any{"title": "عام", "n": 1}}), 204, "")
	if m := ws.next(); !sameJSON(m, `{"t":"d","d":{"a":"d","b":{"p":"rooms/r1","d":{"title":"عام","n":1}}}}`) {
		t.Fatalf("push of the console's set: %s", m)
	}
	expect(t, c.do("PATCH", consoleData("chat-app", "/rooms"), map[string]any{"value": map[string]any{"r1/n": 2, "r2": "two"}}), 204, "")
	if m := ws.next(); !sameJSON(m, `{"t":"d","d":{"a":"m","b":{"p":"rooms","d":{"r1/n":2,"r2":"two"}}}}`) {
		t.Fatalf("push of the console's update: %s", m)
	}
	expect(t, c.do("DELETE", consoleData("chat-app", "/rooms/r2"), nil), 204, "")
	if m := ws.next(); !sameJSON(m, `{"t":"d","d":{"a":"d","b":{"p":"rooms/r2","d":null}}}`) {
		t.Fatalf("push of the console's delete: %s", m)
	}
	// And a client's write shows in the browser.
	ws.put("/rooms/r3", map[string]any{"title": "x"})

	page := c.mustOK(c.do("GET", consoleData("chat-app", "/rooms"), nil))
	children := page.list("children")
	if len(children) != 2 || children[0]["key"] != "r1" || children[0]["hasChildren"] != true || children[1]["key"] != "r3" {
		t.Fatalf("browse /rooms: %s", page.Body)
	}
	leaf := c.mustOK(c.do("GET", consoleData("chat-app", "/rooms/r1/title"), nil))
	assertEqual(t, "leaf value", leaf.JSON["value"], "عام")

	// A big list pages without loading it whole.
	items := map[string]any{}
	for i := range 250 {
		items["-Nk"+strings.Repeat("0", 3-len(itoa(i)))+itoa(i)] = map[string]any{"i": i}
	}
	expect(t, c.do("PUT", consoleData("chat-app", "/big"), map[string]any{"value": items}), 204, "")
	seen, token := 0, ""
	for pages := 0; ; pages++ {
		p := c.mustOK(c.do("GET", consoleData("chat-app", "/big", "pageSize", "100", "pageToken", token), nil))
		seen += len(p.list("children"))
		if token = p.str("nextPageToken"); token == "" {
			assertEqual(t, "pages", pages+1, 3)
			break
		}
	}
	assertEqual(t, "children paged through", seen, 250)

	export := c.do("GET", consoleDB("chat-app")+"/data:export?path=/rooms", nil)
	if export.Status != 200 || !sameJSON(string(export.Body), `{"r1":{"n":2,"title":"عام"},"r3":{"title":"x"}}`) {
		t.Fatalf("export: %d %s", export.Status, export.Body)
	}

	// Invalid keys and values are refused before anything is written.
	expect(t, c.do("PUT", consoleData("chat-app", "/rooms/a.b"), map[string]any{"value": 1}), 400, "INVALID_ARGUMENT")
	expect(t, c.do("PUT", consoleData("chat-app", "/rooms"), map[string]any{"value": map[string]any{"a#b": 1}}), 400, "INVALID_ARGUMENT")
	expect(t, c.do("PUT", consoleData("chat-app", "/rooms/"+strings.Repeat("k", 769)), map[string]any{"value": 1}), 400, "INVALID_ARGUMENT")

	// A CLI token works too, like on the console's other project routes.
	pat := e.withBearer(c.pat())
	pat.mustOK(pat.do("GET", consoleData("chat-app", "/"), nil))
}

func TestRTDBConsoleRules(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	c := e.owner()
	c.project("chat-app")

	got := c.mustOK(c.do("GET", consoleDB("chat-app")+"/rules", nil))
	assertEqual(t, "a new database's rules", got.str("rules"), `{"rules": {".read": false, ".write": false}}`)

	bad := "{\n  \"rules\": {\n    \".read\": true,\n    \".write\": \"auth.uid ==\"\n  }\n}"
	r := c.do("PUT", consoleDB("chat-app")+"/rules", map[string]any{"rules": bad})
	expect(t, r, 400, "INVALID_ARGUMENT")
	var errs []map[string]any
	for _, d := range r.JSON["error"].(map[string]any)["details"].([]any) {
		if d := d.(map[string]any); d["@type"] == "type.aruma.dev/rtdb.RulesErrors" {
			for _, e := range d["errors"].([]any) {
				errs = append(errs, e.(map[string]any))
			}
		}
	}
	if len(errs) != 1 || errs[0]["line"] != float64(4) || errs[0]["path"] != "rules/.write" {
		t.Fatalf("rules errors: %s", r.Body)
	}
	assertEqual(t, "rules after a refused save", c.mustOK(c.do("GET", consoleDB("chat-app")+"/rules", nil)).str("rules"), `{"rules": {".read": false, ".write": false}}`)

	fixed := strings.Replace(bad, `"auth.uid =="`, `"auth != null"`, 1)
	assertEqual(t, "saved rules", c.mustOK(c.do("PUT", consoleDB("chat-app")+"/rules", map[string]any{"rules": fixed})).str("rules"), fixed)
	assertEqual(t, "rules read back", c.mustOK(c.do("GET", consoleDB("chat-app")+"/rules", nil)).str("rules"), fixed)
}

// Tenant isolation: an owner reaches only their own projects' databases.
func TestRTDBConsoleIsolation(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	a, b := e.owner(), e.owner2()
	a.project("chat-app")
	b.project("other-app")
	expect(t, a.do("PUT", consoleData("chat-app", "/secret"), map[string]any{"value": "a's"}), 204, "")

	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", consoleDB("chat-app"), nil},
		{"GET", consoleData("chat-app", "/"), nil},
		{"GET", consoleData("chat-app", "/secret"), nil},
		{"GET", consoleDB("chat-app") + "/data:export?path=/", nil},
		{"PUT", consoleData("chat-app", "/secret"), map[string]any{"value": "b's"}},
		{"PATCH", consoleData("chat-app", "/"), map[string]any{"value": map[string]any{"secret": "b's"}}},
		{"DELETE", consoleData("chat-app", "/secret"), nil},
		{"GET", consoleDB("chat-app") + "/rules", nil},
		{"PUT", consoleDB("chat-app") + "/rules", map[string]any{"rules": `{"rules": {".read": true, ".write": true}}`}},
	} {
		expect(t, b.do(c.method, c.path, c.body), 404, "NOT_FOUND")
	}
	assertEqual(t, "a's data", a.mustOK(a.do("GET", consoleData("chat-app", "/secret"), nil)).JSON["value"], "a's")
	assertEqual(t, "a's rules", a.mustOK(a.do("GET", consoleDB("chat-app")+"/rules", nil)).str("rules"), `{"rules": {".read": false, ".write": false}}`)
	// b's own database is a different one.
	if p := b.mustOK(b.do("GET", consoleData("other-app", "/"), nil)); p.JSON["exists"] != false {
		t.Errorf("b's database: %s", p.Body)
	}
	// Signed out, nothing.
	expect(t, e.client().do("GET", consoleData("chat-app", "/"), nil), 401, "UNAUTHENTICATED")
}

// Disabled, the routes answer, and say so.
func TestRTDBConsoleDisabled(t *testing.T) {
	e := newEnv(t, nil)
	c := e.owner()
	c.project("chat-app")
	info := c.mustOK(c.do("GET", consoleDB("chat-app"), nil))
	assertEqual(t, "enabled", info.JSON["enabled"], false)
	assertEqual(t, "databaseURL", info.str("databaseURL"), e.URL+"/?ns=chat-app-default-rtdb")
	expect(t, c.do("GET", consoleData("chat-app", "/"), nil), 400, "FAILED_PRECONDITION")
}
