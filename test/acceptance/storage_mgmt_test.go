package acceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/platform/rules/rulestest"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// What firebase deploy --only storage and firebase init storage send before and around the
// rules, in their order and shapes (C §1.3, §1.4, §1.5; ADR-022 §15): Service Usage, the default
// bucket, and the firebaserules API, every success 200 exactly. S5-A's file.

func storageMgmtEnv(t *testing.T) *Env {
	t.Helper()
	return newEnv(t, map[string]string{"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": t.TempDir()})
}

const storageRulesSource = `rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    match /public/{all=**} {
      allow read: if true;
    }
  }
}
`

// The routes S5-A adds, with their wildcards filled for chat-app.
func storageMgmtPaths(e *Env, bucket string) [][2]string {
	var out [][2]string
	for _, rt := range e.App.Mgmt.Routes() {
		if !strings.HasPrefix(rt.Pattern, "/v1/") && !strings.HasPrefix(rt.Pattern, "/v1alpha/") && !strings.HasSuffix(rt.Pattern, "/buckets") {
			continue
		}
		path := strings.NewReplacer("{project}", "chat-app", "{ruleset}", "0b0b0b0b-0b0b-4b0b-8b0b-0b0b0b0b0b0b",
			"{release...}", "firebase.storage/"+bucket, "{service}", "firebasestorage.googleapis.com").Replace(rt.Pattern)
		out = append(out, [2]string{rt.Method, path})
	}
	return out
}

func TestStorageDeployRequests(t *testing.T) {
	e := storageMgmtEnv(t)
	owner := e.owner()
	owner.project("chat-app")
	cli := e.withBearer(owner.pat())
	project := cli.mustOK(cli.do("GET", "/v1beta1/projects/chat-app", nil))
	number := project.str("projectNumber")
	bucket := cli.mustOK(cli.do("GET", "/v1beta1/projects/chat-app/adminSdkConfig", nil)).str("storageBucket")
	if bucket == "" || bucket != project.str("resources", "storageBucket") {
		t.Fatalf("storageBucket %q: %s", bucket, project.Body)
	}
	ok := func(method, path string, body any) *Response {
		t.Helper()
		r := cli.do(method, path, body)
		if r.Status != 200 {
			t.Fatalf("%s %s: %d %s", method, path, r.Status, r.Body)
		}
		return r
	}

	// C §1.4: ensure() asks Service Usage for the host FIREBASE_FIREBASESTORAGE_URL names.
	for _, name := range []string{"firebasestorage.googleapis.com", "aruma.example", "127.0.0.1"} {
		s := ok("GET", "/v1/projects/chat-app/services/"+name, nil)
		if s.str("state") != "ENABLED" || s.str("name") != "projects/"+number+"/services/"+name {
			t.Errorf("service %s: %s", name, s.Body)
		}
	}
	en := ok("POST", "/v1/projects/chat-app/services/aruma.example:enable", nil)
	if en.JSON["done"] != true || en.str("response", "service", "state") != "ENABLED" {
		t.Errorf("enable: %s", en.Body)
	}

	// C §1.3: the CLI takes the last segment of bucket.name.
	db := ok("GET", "/v1alpha/projects/chat-app/defaultBucket", nil)
	if name := db.str("bucket", "name"); name != "projects/chat-app/buckets/"+bucket || db.str("name") != "projects/chat-app/defaultBucket" || db.str("location") == "" {
		t.Errorf("defaultBucket: %s", db.Body)
	}
	ok("GET", "/v1alpha/projects/"+number+"/defaultBucket", nil)
	if l := ok("GET", "/v1beta/projects/chat-app/buckets", nil).list("buckets"); len(l) != 1 || l[0]["name"] != "projects/"+number+"/buckets/"+bucket {
		t.Errorf("buckets: %v", l)
	}

	// R1 with a source that compiles: {}. pageToken= empty on the first page (R2).
	files := []map[string]string{{"name": "storage.rules", "content": storageRulesSource}}
	if r := ok("POST", "/v1/projects/chat-app:test", map[string]any{"source": map[string]any{"files": files}}); string(r.Body) != "{}\n" {
		t.Errorf(":test of a good source: %s", r.Body)
	}
	if r := ok("GET", "/v1/projects/chat-app/releases?pageSize=10&pageToken=", nil); string(r.Body) != "{}\n" {
		t.Errorf("releases before any deploy: %s", r.Body)
	}

	// R4, then R5 on a release that does not exist yet is 404, so the CLI goes on to R6.
	created := ok("POST", "/v1/projects/chat-app/rulesets", map[string]any{"source": map[string]any{"files": files}})
	rulesetName := created.str("name")
	if !regexp.MustCompile(`^projects/chat-app/rulesets/[0-9a-f-]{36}$`).MatchString(rulesetName) || created.str("createTime") == "" {
		t.Fatalf("created: %s", created.Body)
	}
	releaseName := "projects/chat-app/releases/firebase.storage/" + bucket
	patch := map[string]any{"release": map[string]any{"name": releaseName, "rulesetName": rulesetName}}
	expect(t, cli.do("PATCH", "/v1/projects/chat-app/releases/firebase.storage/"+bucket, patch), 404, "NOT_FOUND")
	rel := ok("POST", "/v1/projects/chat-app/releases", map[string]any{"name": releaseName, "rulesetName": rulesetName})
	if rel.str("name") != releaseName || rel.str("rulesetName") != rulesetName {
		t.Errorf("created release: %s", rel.Body)
	}
	expect(t, cli.do("POST", "/v1/projects/chat-app/releases", map[string]any{"name": releaseName, "rulesetName": rulesetName}), 409, "ALREADY_EXISTS")

	// The rules apply to the bucket at once.
	if !storageAllows(t, e, "chat-app", bucket, "public/a.png") || storageAllows(t, e, "chat-app", bucket, "private/a.png") {
		t.Error("the released rules do not decide the bucket")
	}

	// The second deploy: R2 finds the release by its prefix, R3 returns exactly the files sent,
	// so the CLI says "already up to date" (C §1.5) and releases the same ruleset with R5.
	releases := ok("GET", "/v1/projects/chat-app/releases?pageSize=10&pageToken=", nil).list("releases")
	if len(releases) != 1 || !strings.HasPrefix(releases[0]["name"].(string), "projects/chat-app/releases/firebase.storage/"+bucket) ||
		releases[0]["rulesetName"] != rulesetName || !rfc3339Micro.MatchString(releases[0]["createTime"].(string)) ||
		!rfc3339Micro.MatchString(releases[0]["updateTime"].(string)) {
		t.Fatalf("releases: %v", releases)
	}
	got := ok("GET", "/v1/"+rulesetName, nil)
	var content struct {
		Source struct {
			Files []map[string]any `json:"files"`
		} `json:"source"`
	}
	if err := json.Unmarshal(got.Body, &content); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(content.Source.Files, []map[string]any{{"name": "storage.rules", "content": storageRulesSource}}) {
		t.Errorf("the ruleset's files are not what was sent: %s", got.Body)
	}
	ok("PATCH", "/v1/projects/chat-app/releases/firebase.storage/"+bucket, patch)
	got2 := ok("GET", "/v1/projects/chat-app/releases/firebase.storage/"+bucket, nil)
	if got2.str("rulesetName") != rulesetName {
		t.Errorf("get release: %s", got2.Body)
	}

	// A changed source: a new ruleset, released by R5.
	files2 := []map[string]string{{"name": "storage.rules", "content": strings.Replace(storageRulesSource, "public", "open", 1)}}
	second := ok("POST", "/v1/projects/chat-app/rulesets", map[string]any{"source": map[string]any{"files": files2}}).str("name")
	updated := ok("PATCH", "/v1/projects/chat-app/releases/firebase.storage/"+bucket,
		map[string]any{"release": map[string]any{"name": releaseName, "rulesetName": second}})
	if updated.str("rulesetName") != second || updated.str("name") != releaseName {
		t.Errorf("updated release: %s", updated.Body)
	}
	if storageAllows(t, e, "chat-app", bucket, "public/a.png") || !storageAllows(t, e, "chat-app", bucket, "open/a.png") {
		t.Error("the updated release does not decide the bucket")
	}

	// R7 and R8, as the CLI uses them past the ruleset limit: names and times, newest first
	// after sorting, and the released ruleset stays.
	list := ok("GET", "/v1/projects/chat-app/rulesets?pageSize=100&pageToken=", nil)
	sets := list.list("rulesets")
	if len(sets) != 2 || strings.Contains(string(list.Body), "source") {
		t.Fatalf("rulesets: %s", list.Body)
	}
	names := []string{sets[0]["name"].(string), sets[1]["name"].(string)}
	slices.Sort(names)
	want := []string{rulesetName, second}
	slices.Sort(want)
	if !slices.Equal(names, want) || !rfc3339Micro.MatchString(sets[0]["createTime"].(string)) {
		t.Errorf("rulesets: %s", list.Body)
	}
	expect(t, cli.do("DELETE", "/v1/"+second, nil), 400, "FAILED_PRECONDITION")
	if r := ok("DELETE", "/v1/"+rulesetName, nil); string(r.Body) != "{}\n" {
		t.Errorf("delete: %s", r.Body)
	}
	expect(t, cli.do("GET", "/v1/"+rulesetName, nil), 404, "NOT_FOUND")
	expect(t, cli.do("DELETE", "/v1/"+rulesetName, nil), 404, "NOT_FOUND")

	// Paging: one ruleset a page, then a token, then the end.
	ok("POST", "/v1/projects/chat-app/rulesets", map[string]any{"source": map[string]any{"files": files}})
	p1 := ok("GET", "/v1/projects/chat-app/rulesets?pageSize=1", nil)
	tok := p1.str("nextPageToken")
	if len(p1.list("rulesets")) != 1 || tok == "" {
		t.Fatalf("first page: %s", p1.Body)
	}
	p2 := ok("GET", "/v1/projects/chat-app/rulesets?pageSize=1&pageToken="+tok, nil)
	if len(p2.list("rulesets")) != 1 || p2.str("nextPageToken") != "" || p2.list("rulesets")[0]["name"] == p1.list("rulesets")[0]["name"] {
		t.Errorf("second page: %s", p2.Body)
	}
	expect(t, cli.do("GET", "/v1/projects/chat-app/rulesets?filter=name%3Dx", nil), 400, "INVALID_ARGUMENT")
}

// rfc3339Micro is the createTime firebase-tools compares as text.
var rfc3339Micro = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{6}Z$`)

// storageAllows decides a get of name on the bucket by the project's released Storage rules.
func storageAllows(t *testing.T, e *Env, projectID, bucket, name string) bool {
	t.Helper()
	ctx := context.Background()
	var number int64
	if err := e.Pool.QueryRow(ctx, `SELECT project_number FROM control.projects WHERE project_id = $1`, projectID).Scan(&number); err != nil {
		t.Fatal(err)
	}
	rs, err := e.App.Service.CompiledRelease(ctx, number, tcore.StorageRelease)
	if err != nil {
		t.Fatal(err)
	}
	segs := append([]string{"b", bucket, "o"}, strings.Split(name, "/")...)
	d, err := rs.Evaluate(ctx, &rules.Request{Project: number, Method: rules.MethodGet, Path: rules.Literals(segs...),
		Variables: map[string]rules.Value{"request": rules.Map(nil), "resource": rules.Null()},
		Documents: rules.NoDocuments{}, Budget: rules.NewBudget(), Inflight: rules.NewInflight(1 << 20)})
	if err != nil {
		t.Fatal(err)
	}
	return d.Allow
}

// R1 on a source that does not compile is 200 with issues[], which firebase deploy prints as
// "[E] line:column - description". Every compile error the production Rules Test API returned to
// S0-A and S1-E (test/oracle/rules/production/raw) comes back the same, field for field.
func TestStorageTestAPIMatchesProductionIssues(t *testing.T) {
	e := storageMgmtEnv(t)
	owner := e.owner()
	owner.project("chat-app")
	cli := e.withBearer(owner.pat())

	syntax := cli.do("POST", "/v1/projects/chat-app:test", map[string]any{"source": map[string]any{"files": []map[string]string{
		{"name": "storage.rules", "content": "rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n    allow read: if ;\n  }\n}\n"}}}})
	if syntax.Status != 200 {
		t.Fatalf("syntax error: %d %s", syntax.Status, syntax.Body)
	}
	issues := syntax.list("issues")
	if len(issues) == 0 || issues[0]["severity"] != "ERROR" || issues[0]["description"] == "" {
		t.Fatalf("issues: %s", syntax.Body)
	}
	pos, _ := issues[0]["sourcePosition"].(map[string]any)
	if pos["fileName"] != "storage.rules" || pos["line"] != float64(4) || pos["column"] == nil {
		t.Errorf("sourcePosition: %s", syntax.Body)
	}

	paths, err := filepath.Glob("../oracle/rules/production/raw/*.json")
	if err != nil || len(paths) == 0 {
		t.Fatalf("the oracle's raw captures: %v", err)
	}
	type call struct {
		ID      string `json:"id"`
		Request struct {
			Body     json.RawMessage `json:"body"`
			BodyText string          `json:"bodyText"` // a body JSON.stringify could not write
		} `json:"request"`
		Status   int             `json:"status"`
		Response json.RawMessage `json:"response"`
	}
	compared, exact, tooLarge := 0, 0, 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var file struct {
			Calls []call `json:"calls"`
		}
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for i, c := range file.Calls {
			var want testAPIResponse
			_ = json.Unmarshal(c.Response, &want)
			sized := c.Status == 400 && want.Error != nil && strings.HasPrefix(want.Error.Message, "Total size of sources")
			if !(c.Status == 200 && len(want.errorLines()) > 0) && !sized {
				continue
			}
			key := filepath.Base(path) + "#" + strconv.Itoa(i)
			body := c.Request.Body
			if c.Request.BodyText != "" {
				body = json.RawMessage(c.Request.BodyText)
			}
			resp := cli.do("POST", "/v1/projects/chat-app:test", rebuiltBody(t, body))
			var got testAPIResponse
			_ = json.Unmarshal(resp.Body, &got)
			switch {
			case resp.Status != c.Status:
				t.Errorf("%s %s: got %d %s, want %d %s", key, c.ID, resp.Status, resp.Body, c.Status, c.Response)
			case sized:
				if got.Error == nil || got.Error.Message != want.Error.Message || got.Error.Status != "INVALID_ARGUMENT" {
					t.Errorf("%s %s: %s, want %s", key, c.ID, resp.Body, c.Response)
				}
				tooLarge++
			default:
				if g, w := got.errorLines(), want.errorLines(); !slices.Equal(g, w) && !engineErrorDifferences[key] {
					t.Errorf("%s %s: firebase deploy would print\n%s\nwant\n%s", key, c.ID, strings.Join(g, "\n"), strings.Join(w, "\n"))
				}
				compared++
			}
			if sameJSONBytes(resp.Body, c.Response) {
				exact++
			}
		}
	}
	// The rest differ in what the engine reports, not in this API's shape: endOffset spans, the
	// unused-variable and version warnings, and type warnings the engine does not give
	// (ADR-021 §2.1).
	t.Logf("%d compile errors and %d sources over the size limit compared with production; %d byte for byte", compared, tooLarge, exact)
	if compared < 330 || tooLarge < 9 || exact < 320 {
		t.Errorf("%d compile errors, %d size errors, %d exact: fewer than the oracle holds", compared, tooLarge, exact)
	}
}

// engineErrorDifferences are the captures where the engine reports another syntax error than
// production, an extra "Unexpected ')'." after a map literal with a double key: the rules
// engine's (internal/platform/rules), not this API's.
var engineErrorDifferences = map[string]bool{
	"hidane-conditions.json#13": true, "hidane-conditions.json#14": true,
	"hidane-conditions.json#89": true, "hidane-conditions.json#90": true,
}

type testAPIResponse struct {
	Issues []struct {
		SourcePosition struct {
			Line   int `json:"line"`
			Column int `json:"column"`
		} `json:"sourcePosition"`
		Description string `json:"description"`
		Severity    string `json:"severity"`
	} `json:"issues"`
	Error *struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// errorLines are the errors as firebase deploy prints them (rulesDeploy.ts:313-332).
func (r testAPIResponse) errorLines() []string {
	var out []string
	for _, i := range r.Issues {
		if i.Severity == "ERROR" {
			out = append(out, "[E] "+strconv.Itoa(i.SourcePosition.Line)+":"+strconv.Itoa(i.SourcePosition.Column)+" - "+i.Description)
		}
	}
	return out
}

// rebuiltBody returns a recorded :test body as it was sent: a generated file, which the capture
// keeps as its generator and digest (test/oracle/rules/tools/record-production.mjs), is generated
// again and checked against the digest.
func rebuiltBody(t *testing.T, body json.RawMessage) json.RawMessage {
	t.Helper()
	var b map[string]any
	if err := json.Unmarshal(body, &b); err != nil {
		t.Fatal(err)
	}
	src, _ := b["source"].(map[string]any)
	files, _ := src["files"].([]any)
	generated := false
	for _, f := range files {
		file, _ := f.(map[string]any)
		gen, ok := file["content"].(map[string]any)
		if !ok {
			continue
		}
		generated = true
		raw, _ := json.Marshal(gen["generated"])
		var g rulestest.Generator
		if err := json.Unmarshal(raw, &g); err != nil {
			t.Fatal(err)
		}
		content, err := g.Generate()
		if err != nil {
			t.Fatal(err)
		}
		if sum := sha256.Sum256([]byte(content)); hex.EncodeToString(sum[:]) != gen["sha256"] {
			t.Fatalf("the generated file does not match its digest: %v", gen)
		}
		file["content"] = content
	}
	if !generated {
		// As recorded, byte for byte (a bodyText holds numbers such as 2.0).
		return body
	}
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sameJSONBytes reports whether two JSON texts hold the same value, key order aside.
func sameJSONBytes(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

// The refusals around R1 and R4: more than one file, no source, test cases (not run, so not
// answered as passed), a named Firestore database, and a source that does not compile, whose
// create is refused with its errors.
func TestStorageRulesAPIRefusals(t *testing.T) {
	e := storageMgmtEnv(t)
	owner := e.owner()
	owner.project("chat-app")
	cli := e.withBearer(owner.pat())
	file := map[string]string{"name": "storage.rules", "content": storageRulesSource}
	two := map[string]any{"source": map[string]any{"files": []map[string]string{file, file}}}
	for _, path := range []string{"/v1/projects/chat-app:test", "/v1/projects/chat-app/rulesets"} {
		r := cli.do("POST", path, two)
		expect(t, r, 400, "INVALID_ARGUMENT")
		if msg := r.str("error", "message"); msg != "Currently only one file source can be passed to firebase rules." {
			t.Errorf("%s: %s", path, msg)
		}
		expect(t, cli.do("POST", path, map[string]any{}), 400, "INVALID_ARGUMENT")
	}
	expect(t, cli.do("POST", "/v1/projects/chat-app:test", map[string]any{"source": map[string]any{"files": []any{file}},
		"testSuite": map[string]any{"testCases": []any{map[string]any{"expectation": "ALLOW"}}}}), 400, "INVALID_ARGUMENT")
	expect(t, cli.do("POST", "/v1/projects/chat-app/rulesets", map[string]any{"source": map[string]any{"files": []any{file}},
		"attachment_point": "firestore.googleapis.com/projects/1/databases/named"}), 400, "INVALID_ARGUMENT")

	bad := cli.do("POST", "/v1/projects/chat-app/rulesets", map[string]any{"source": map[string]any{"files": []map[string]string{
		{"name": "storage.rules", "content": "rules_version = '2';\nservice firebase.storage {\n  match /b/{bucket}/o {\n    allow read: if ;\n  }\n}\n"}}}})
	expect(t, bad, 400, "INVALID_ARGUMENT")
	if !regexp.MustCompile(`\[E\] 4:\d+ - `).MatchString(bad.str("error", "message")) {
		t.Errorf("the refused create does not say where: %s", bad.Body)
	}
	if l := cli.mustOK(cli.do("GET", "/v1/projects/chat-app/rulesets", nil)).list("rulesets"); len(l) != 0 {
		t.Errorf("a refused source was saved: %v", l)
	}

	// The release takes its own service's ruleset; a Firestore source compiles as Firestore.
	fs := map[string]any{"source": map[string]any{"files": []map[string]string{{"name": "firestore.rules",
		"content": "rules_version = '2';\nservice cloud.firestore {\n  match /databases/{database}/documents {\n    match /{d=**} {\n      allow read: if false;\n    }\n  }\n}\n"}}}}
	if r := cli.mustOK(cli.do("POST", "/v1/projects/chat-app:test", fs)); string(r.Body) != "{}\n" {
		t.Errorf(":test of a Firestore source: %s", r.Body)
	}
	fsName := cli.mustOK(cli.do("POST", "/v1/projects/chat-app/rulesets", fs)).str("name")
	cli.mustOK(cli.do("POST", "/v1/projects/chat-app/releases", map[string]any{"name": "projects/chat-app/releases/cloud.firestore", "rulesetName": fsName}))
	bucket := cli.mustOK(cli.do("GET", "/v1beta1/projects/chat-app", nil)).str("resources", "storageBucket")
	expect(t, cli.do("POST", "/v1/projects/chat-app/releases", map[string]any{"name": "projects/chat-app/releases/firebase.storage/" + bucket, "rulesetName": fsName}), 400, "INVALID_ARGUMENT")

	// Release names: under the request's project, matching the path, the bucket the project has.
	for _, body := range []map[string]any{
		{"name": "projects/other-app/releases/cloud.firestore", "rulesetName": fsName},
		{"name": "cloud.firestore", "rulesetName": fsName},
		{"name": "projects/chat-app/releases/cloud.firestore/x"},
	} {
		expect(t, cli.do("POST", "/v1/projects/chat-app/releases", body), 400, "INVALID_ARGUMENT")
	}
	expect(t, cli.do("POST", "/v1/projects/chat-app/releases", map[string]any{"name": "projects/chat-app/releases/firebase.storage/other-app.example", "rulesetName": fsName}), 404, "NOT_FOUND")
	expect(t, cli.do("PATCH", "/v1/projects/chat-app/releases/cloud.firestore", map[string]any{"release": map[string]any{
		"name": "projects/chat-app/releases/custom", "rulesetName": fsName}}), 400, "INVALID_ARGUMENT")
	expect(t, cli.do("PATCH", "/v1/projects/chat-app/releases/cloud.firestore?updateMask=name", map[string]any{"release": map[string]any{
		"rulesetName": fsName}}), 400, "INVALID_ARGUMENT")
	cli.mustOK(cli.do("PATCH", "/v1/projects/chat-app/releases/cloud.firestore?updateMask=ruleset_name", map[string]any{"release": map[string]any{
		"rulesetName": fsName}}))
}

// Every S5-A route needs a credential; another account's project and an unknown one are
// NOT_FOUND; a service account reaches its own project only; without Storage none is served.
func TestStorageMgmtAuthAndIsolation(t *testing.T) {
	e := storageMgmtEnv(t)
	owner := e.owner()
	owner.project("chat-app")
	other := e.withBearer(e.account("stranger@example.com").pat())
	cli := e.withBearer(owner.pat())
	bucket := cli.mustOK(cli.do("GET", "/v1beta1/projects/chat-app", nil)).str("resources", "storageBucket")
	paths := storageMgmtPaths(e, bucket)
	if len(paths) != 13 {
		t.Fatalf("%d S5-A routes: %v", len(paths), paths)
	}
	for _, mp := range paths {
		if r := e.client().do(mp[0], mp[1], map[string]any{}); r.Status != 401 || r.errStatus() != "UNAUTHENTICATED" {
			t.Errorf("%s %s anonymous: %d %s", mp[0], mp[1], r.Status, r.Body)
		}
		if r := other.do(mp[0], mp[1], map[string]any{}); r.Status != 404 || r.errStatus() != "NOT_FOUND" {
			t.Errorf("%s %s by another account: %d %s", mp[0], mp[1], r.Status, r.Body)
		}
		nope := strings.Replace(mp[1], "chat-app", "nope-project", 1)
		if r := cli.do(mp[0], nope, map[string]any{}); r.Status != 404 {
			t.Errorf("%s %s: %d %s", mp[0], nope, r.Status, r.Body)
		}
	}
	// What one account saved, another never reads, lists or deletes.
	file := map[string]any{"source": map[string]any{"files": []map[string]string{{"name": "storage.rules", "content": storageRulesSource}}}}
	name := cli.mustOK(cli.do("POST", "/v1/projects/chat-app/rulesets", file)).str("name")
	other.project("other-app")
	expect(t, other.do("GET", "/v1/"+name, nil), 404, "NOT_FOUND")
	expect(t, other.do("GET", "/v1/projects/other-app/rulesets/"+name[strings.LastIndex(name, "/")+1:], nil), 404, "NOT_FOUND")
	expect(t, other.do("DELETE", "/v1/projects/other-app/rulesets/"+name[strings.LastIndex(name, "/")+1:], nil), 404, "NOT_FOUND")
	expect(t, other.do("POST", "/v1/projects/other-app/releases", map[string]any{"name": "projects/other-app/releases/cloud.firestore", "rulesetName": name}), 404, "NOT_FOUND")
	if r := other.mustOK(other.do("GET", "/v1/projects/other-app/rulesets", nil)); string(r.Body) != "{}\n" {
		t.Errorf("another project's rulesets: %s", r.Body)
	}

	// A service account of chat-app: its own project, not another.
	_, keyFile := owner.serviceAccountKey("chat-app", "deployer")
	token, _, err := exchange(t, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	sa := e.withBearer(token)
	sa.mustOK(sa.do("GET", "/v1alpha/projects/chat-app/defaultBucket", nil))
	sa.mustOK(sa.do("GET", "/v1/"+name, nil))
	expect(t, sa.do("GET", "/v1alpha/projects/other-app/defaultBucket", nil), 404, "NOT_FOUND")

	// Without Storage the Management API registers none of them, and the server routes /v1/ and
	// /v1alpha/ elsewhere (server.go): nothing answers 2xx, so defaultBucket's failure makes the
	// CLI stop before any rules request.
	off := newEnv(t, nil)
	offOwner := off.owner()
	offOwner.project("chat-app")
	pat := offOwner.pat()
	offCLI := off.withBearer(pat)
	for _, mp := range paths {
		if r := offCLI.do(mp[0], mp[1], map[string]any{}); r.Status < 400 {
			t.Errorf("without Storage, %s %s: %d %s", mp[0], mp[1], r.Status, r.Body)
		}
		req := httptest.NewRequest(mp[0], mp[1], strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+pat)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		off.App.Mgmt.Handler().ServeHTTP(rec, req)
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), `"NOT_FOUND"`) {
			t.Errorf("without Storage, the Management API on %s %s: %d %s", mp[0], mp[1], rec.Code, rec.Body)
		}
	}
}
