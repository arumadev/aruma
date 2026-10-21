package acceptance

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// restAs sends a REST request to the test server with the credential in Authorization, if any,
// and to host, if any, in the Host header.
func restAs(t *testing.T, e *Env, method, target, body, bearer string, header map[string]string) restReply {
	t.Helper()
	h := http.Header{}
	if bearer != "" {
		h.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range header {
		h.Set(k, v)
	}
	return restDo(t, e, method, target, body, h)
}

// A REST write goes through the database's writer like an SDK's: a listener on a WebSocket
// sees it as it commits, and a REST read sees the listener's own write.
func TestRTDBRESTWritesReachListeners(t *testing.T) {
	e := rtdbEnv(t, false, nil)
	ns := e.owner().projectNS("chat-app")
	ws := e.dial(ns, dialOpts{})
	ws.call("q", map[string]any{"p": "/room", "h": ""}).mustOK(t)

	if r := restAs(t, e, "PUT", "/room/a.json?ns="+ns, `{"t":{".sv":"timestamp"}}`, "", nil); r.status != 200 {
		t.Fatalf("PUT: %d %s", r.status, r.body)
	}
	if m := ws.next(); !strings.Contains(m, `"p":"room/a"`) || !strings.Contains(m, `"t":1`) {
		t.Errorf("listener after PUT: %s", m)
	}
	if r := restAs(t, e, "PATCH", "/room.json?ns="+ns, `{"b/c":1,"a":null}`, "", nil); r.status != 200 || !sameJSON(r.body, `{"b":{"c":1},"a":null}`) {
		t.Fatalf("PATCH: %d %s", r.status, r.body)
	}
	if m := ws.next(); !sameJSON(m, `{"t":"d","d":{"a":"m","b":{"p":"room","d":{"a":null,"b/c":1}}}}`) {
		t.Errorf("listener after PATCH: %s", m)
	}
	r := restAs(t, e, "POST", "/room/list.json?ns="+ns, `"hi"`, "", nil)
	if r.status != 200 || !pushIDIn.MatchString(r.body) {
		t.Fatalf("POST: %d %s", r.status, r.body)
	}
	if m := ws.next(); !strings.Contains(m, `"p":"room/list/-`) || !strings.Contains(m, `"d":"hi"`) {
		t.Errorf("listener after POST: %s", m)
	}
	if r := restAs(t, e, "DELETE", "/room/b.json?ns="+ns, "", "", nil); r.status != 200 || r.body != "null" {
		t.Fatalf("DELETE: %d %s", r.status, r.body)
	}
	if m := ws.next(); !sameJSON(m, `{"t":"d","d":{"a":"d","b":{"p":"room/b","d":null}}}`) {
		t.Errorf("listener after DELETE: %s", m)
	}
	ws.put("/room/mine", 7)
	if r := restAs(t, e, "GET", "/room/mine.json?ns="+ns, "", "", nil); r.body != "7" {
		t.Errorf("GET after the listener's write: %s", r.body)
	}
}

// The credentials of REST under rules, as production takes them (wire golden
// production/rest_auth.json): a service account's access token and the owner's CLI token, as
// a bearer or ?access_token=, bypass them, auth_variable_override beside them acts as that
// auth variable, and an Aruma user's ID token in ?auth= is held to them. A user's ID token as
// a bearer is refused; a ?auth= that does not verify is ignored.
func TestRTDBRESTCredentials(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	ns, users, pat, _ := rulesSetUp(t, e)
	project := strings.TrimSuffix(ns, "-default-rtdb")
	_, key := users.owner.serviceAccountKey(project, "rest-backend")
	sat, _, err := exchange(t, key)
	if err != nil {
		t.Fatal(err)
	}
	for name, cred := range map[string]string{"service account": sat, "CLI token": pat} {
		if r := restAs(t, e, "PUT", "/users/amal.json?ns="+ns, `{"age":30}`, cred, nil); r.status != 200 {
			t.Errorf("%s PUT: %d %s", name, r.status, r.body)
		}
	}
	if r := restAs(t, e, "GET", "/users/amal.json?ns="+ns+"&access_token="+url.QueryEscape(sat), "", "", nil); r.status != 200 || r.body != `{"age":30}` {
		t.Errorf("access_token=: %d %s", r.status, r.body)
	}
	// The Go and Python Admin SDKs' databaseAuthVariableOverride.
	override := func(v string) string { return "&auth_variable_override=" + url.QueryEscape(v) }
	if r := restAs(t, e, "GET", "/users/amal.json?ns="+ns+override(`{"uid":"amal"}`), "", sat, nil); r.status != 200 {
		t.Errorf("override amal: %d %s", r.status, r.body)
	}
	for _, v := range []string{`{"uid":"badr"}`, `null`} {
		if r := restAs(t, e, "GET", "/users/amal.json?ns="+ns+override(v), "", sat, nil); r.status != 401 {
			t.Errorf("override %s: %d %s", v, r.status, r.body)
		}
	}
	if r := restAs(t, e, "GET", "/users/amal.json?ns="+ns+override(`"amal"`), "", sat, nil); r.status != 400 {
		t.Errorf("override not an object: %d %s", r.status, r.body)
	}
	// Without an admin credential the override is ignored, as on the emulator.
	if r := restAs(t, e, "GET", "/users/amal.json?ns="+ns+override(`{"uid":"amal"}`), "", "", nil); r.status != 401 {
		t.Errorf("override alone: %d %s", r.status, r.body)
	}

	users.create(project, "amal")
	idToken := users.signIn(project, "amal").str("idToken")
	if r := restAs(t, e, "GET", "/users/amal.json?ns="+ns+"&auth="+idToken, "", "", nil); r.status != 200 || r.body != `{"age":30}` {
		t.Errorf("own profile with ?auth=: %d %s", r.status, r.body)
	}
	if r := restAs(t, e, "PATCH", "/users/amal.json?ns="+ns+"&auth="+idToken, `{"age":"old"}`, "", nil); r.status != 401 {
		t.Errorf(".validate over REST: %d %s", r.status, r.body)
	}
	if r := restAs(t, e, "PUT", "/users/badr.json?ns="+ns+"&auth="+idToken, `{"age":1}`, "", nil); r.status != 401 || !strings.Contains(r.body, "Permission denied") {
		t.Errorf("another's profile: %d %s", r.status, r.body)
	}
	// A bearer or access_token that is not an admin credential is refused, a user's ID token
	// included; a ?auth= that does not verify acts as nobody, which these rules deny.
	for _, c := range []struct{ query, bearer, want string }{
		{"", idToken, "Unauthorized request."},
		{"&access_token=" + idToken, "", "Unauthorized request."},
		{"&access_token=aruma_sat_nope", "", "Unauthorized request."},
		{"&auth=garbage", "", "Permission denied"},
		{"&auth=" + pat, "", "Permission denied"},
	} {
		if r := restAs(t, e, "GET", "/users/amal.json?ns="+ns+c.query, "", c.bearer, nil); r.status != 401 || !strings.Contains(r.body, c.want) {
			t.Errorf("%.30s %.10s: %d %s", c.query, c.bearer, r.status, r.body)
		}
	}
}

// The database host serves REST as the emulator does, and the main host with ?ns= the same
// database. Streaming is refused (ADR-020 §3), and a body over rest_write_bytes too.
func TestRTDBRESTRoutes(t *testing.T) {
	e := rtdbEnv(t, false, map[string]string{"ARUMA_RTDB_DOMAIN": "rtdb.aruma.test", "ARUMA_RTDB_REST_WRITE_BYTES": "1048576"})
	e.owner().project("chat-app")
	host := map[string]string{"Host": "chat-app-default-rtdb.rtdb.aruma.test"}
	send := func(method, target, body string, header map[string]string) restReply {
		t.Helper()
		req, err := http.NewRequest(method, e.URL+target, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range header {
			req.Header.Set(k, v)
		}
		if h := header["Host"]; h != "" {
			req.Host = h
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return restReply{status: resp.StatusCode, header: resp.Header, body: string(b)}
	}
	if r := send("PUT", "/x.json", `{"a":1}`, host); r.status != 200 {
		t.Fatalf("PUT on the database host: %d %s", r.status, r.body)
	}
	if r := send("GET", "/x/a.json?ns=chat-app-default-rtdb", "", nil); r.body != "1" {
		t.Errorf("same database on the main host: %s", r.body)
	}
	if r := send("GET", "/x", "", host); r.status != 400 || r.body != "Append .json to your URL to use the REST API." {
		t.Errorf("no .json: %d %s", r.status, r.body)
	}
	if r := send("GET", "/.info", "", host); r.status != 404 {
		t.Errorf("/.info: %d %s", r.status, r.body)
	}
	if r := send("GET", "/x.json", "", map[string]string{"Host": host["Host"], "Accept": "text/event-stream"}); r.status != 400 ||
		!strings.Contains(r.body, "Streaming") {
		t.Errorf("streaming: %d %s", r.status, r.body)
	}
	big := `"` + strings.Repeat("a", 1<<20) + `"`
	if r := send("PUT", "/big.json", big, host); r.status != 400 || !strings.Contains(r.body, "exceeds the maximum size") {
		t.Errorf("over rest_write_bytes: %d %.200s", r.status, r.body)
	}
	// A timeout bounds the wait; a read this small never reaches it.
	if r := send("GET", "/x.json?timeout=5s", "", host); r.status != 200 {
		t.Errorf("timeout=5s: %d %s", r.status, r.body)
	}
}

// firebase deploy --only database --dry-run checks the rules and saves nothing; the Admin
// SDKs' getRulesJSON reads them without comments.
func TestRTDBRESTRulesDryRunAndStrict(t *testing.T) {
	e := rtdbEnv(t, true, nil)
	owner := e.owner()
	ns := owner.projectNS("chat-app")
	pat := owner.pat()
	doc := "{\n  // signed-in users only\n  \"rules\": {\".read\": \"auth != null\", \".write\": false,}\n}"
	e.putRules(pat, ns, doc)
	rules := "/.settings/rules.json?ns=" + ns
	if r := restAs(t, e, "PUT", rules+"&dryRun=true", `{"rules": {".read": true}}`, pat, nil); r.status != 200 || r.body != `{"status":"ok"}` {
		t.Errorf("dry run: %d %s", r.status, r.body)
	}
	if r := restAs(t, e, "PUT", rules+"&dryRun=true", `{"rules": {".read": "auth !="}}`, pat, nil); r.status != 400 ||
		!strings.HasPrefix(r.body, "{\n  \"error\" : \"1:") {
		t.Errorf("dry run of bad rules: %d %s", r.status, r.body)
	}
	if r := restAs(t, e, "GET", rules, "", pat, nil); r.body != doc {
		t.Errorf("after the dry runs: %s", r.body)
	}
	if r := restAs(t, e, "GET", rules+"&format=strict", "", pat, nil); !sameJSON(r.body, `{"rules":{".read":"auth != null",".write":false}}`) {
		t.Errorf("format=strict: %s", r.body)
	}
	if r := restAs(t, e, "GET", rules, "", "", nil); r.status != 403 {
		t.Errorf("without a credential: %d %s", r.status, r.body)
	}
}

// firebase-tools finds a project's database through the management API before any database
// command or deploy: the project's realtimeDatabaseInstance, then that instance's databaseUrl
// (management/database.ts). Another instance, another account's project and a server without
// the Realtime Database find nothing.
func TestRTDBManagementInstances(t *testing.T) {
	e := rtdbEnv(t, false, map[string]string{"ARUMA_RTDB_DOMAIN": "rtdb.aruma.test"})
	owner := e.owner()
	owner.project("chat-app")
	cli := e.withBearer(owner.pat())
	p := cli.mustOK(cli.do("GET", "/v1beta1/projects/chat-app", nil))
	if got := p.str("resources", "realtimeDatabaseInstance"); got != "chat-app-default-rtdb" {
		t.Errorf("realtimeDatabaseInstance %q: %s", got, p.Body)
	}
	inst := cli.mustOK(cli.do("GET", "/v1beta/projects/chat-app/locations/-/instances/chat-app-default-rtdb", nil))
	want := "projects/" + p.str("projectNumber") + "/locations/us-central1/instances/chat-app-default-rtdb"
	if inst.str("name") != want || inst.str("databaseUrl") != "https://chat-app-default-rtdb.rtdb.aruma.test" ||
		inst.str("type") != "DEFAULT_DATABASE" || inst.str("state") != "ACTIVE" {
		t.Errorf("instance: %s", inst.Body)
	}
	if l := cli.mustOK(cli.do("GET", "/v1beta/projects/chat-app/locations/-/instances", nil)).list("instances"); len(l) != 1 {
		t.Errorf("instances: %v", l)
	}
	for _, path := range []string{
		"/v1beta/projects/chat-app/locations/-/instances/other-default-rtdb",
		"/v1beta/projects/chat-app/locations/europe-west1/instances/chat-app-default-rtdb",
	} {
		if resp := cli.do("GET", path, nil); resp.Status != 404 {
			t.Errorf("%s: %d", path, resp.Status)
		}
	}
	other := e.withBearer(e.owner2().pat())
	if resp := other.do("GET", "/v1beta/projects/chat-app/locations/-/instances/chat-app-default-rtdb", nil); resp.Status != 404 {
		t.Errorf("another account's project: %d", resp.Status)
	}
	if resp := e.client().do("GET", "/v1beta/projects/chat-app/locations/-/instances/chat-app-default-rtdb", nil); resp.Status != 401 {
		t.Errorf("no credential: %d", resp.Status)
	}

	off := newEnv(t, nil)
	offOwner := off.owner()
	offOwner.project("chat-app")
	offCLI := off.withBearer(offOwner.pat())
	if body := offCLI.mustOK(offCLI.do("GET", "/v1beta1/projects/chat-app", nil)).Body; strings.Contains(string(body), "realtimeDatabaseInstance") {
		t.Errorf("RTDB off, the project names a database: %s", body)
	}
	if resp := offCLI.do("GET", "/v1beta/projects/chat-app/locations/-/instances/chat-app-default-rtdb", nil); resp.Status != 404 {
		t.Errorf("RTDB off: %d", resp.Status)
	}
}
