package rulestest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/rules"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func caseFile(set, id string) string {
	return `{"schema": "aruma.rules-oracle/v1", "topic": "t", "service": "firestore", "set": "` + set + `",
	 "source": "aruma", "provenance": "rules-test-api", "recordedAt": "2026-10-24", "version": "v",
	 "rulesets": {}, "resources": {},
	 "cases": [{"id": "` + id + `", "expression": "true",
	  "ruleset": "rules_version = '2';\nservice cloud.firestore { match /databases/{d}/documents { match /c/{x} { allow get: if true; } } }",
	  "request": {"method": "get", "path": "/databases/(default)/documents/c/x"},
	  "expected": {"outcome": "allow"}}]}`
}

// TestLoadSkipsHeldOut proves that no case under a directory named heldout reaches the set,
// whatever the directory holds and wherever it is, and that a held-out file outside such a
// directory is refused rather than read.
func TestLoadSkipsHeldOut(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "production", "dev.json"), caseFile("development", "aruma/t/dev"))
	// Held-out cases in the layout of the oracle-rules-heldout branch, and deeper.
	writeFile(t, filepath.Join(root, "heldout", "production", "t.json"), caseFile("development", "aruma/t/secret1"))
	writeFile(t, filepath.Join(root, "imported", "x", "heldout", "t.json"), caseFile("development", "aruma/t/secret2"))
	// Load must not even read these: they do not parse, and an emulator file that names a
	// case nobody has would fail the load.
	writeFile(t, filepath.Join(root, "heldout", "broken.json"), "{not json")
	writeFile(t, filepath.Join(root, "heldout", "emulator", "t.json"),
		`{"schema": "aruma.rules-oracle/v1", "provenance": "emulator", "cases": [{"id": "nobody", "emulator": {"outcome": "allow"}}]}`)

	set, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Cases) != 1 || set.Cases[0].ID != "aruma/t/dev" {
		var ids []string
		for _, c := range set.Cases {
			ids = append(ids, c.ID)
		}
		t.Fatalf("cases %v, want only aruma/t/dev", ids)
	}
	for _, f := range set.Files {
		if strings.Contains(f.Path, "heldout") {
			t.Errorf("read %s", f.Path)
		}
	}

	// A file that says it is held out, outside a heldout directory, is an error, not a case.
	writeFile(t, filepath.Join(root, "production", "misplaced.json"), caseFile("heldout", "aruma/t/secret3"))
	if _, err := Load(root); !errors.Is(err, ErrHeldOut) {
		t.Fatalf("Load with a misplaced held-out file: %v, want ErrHeldOut", err)
	}
}

func loadOracle(t *testing.T) *Set {
	t.Helper()
	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	set, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// TestLoadDevelopmentOracle reads the oracle on this branch: every file, every source and
// provenance, and every case either binds or says why it cannot.
func TestLoadDevelopmentOracle(t *testing.T) {
	set := loadOracle(t)
	shapes := map[string]int{}
	for _, f := range set.Files {
		shapes[f.Shape]++
		if f.SHA256 == "" || f.Schema != Schema {
			t.Errorf("%s: schema %q, sha256 %q", f.Path, f.Schema, f.SHA256)
		}
	}
	if shapes["cases"] < 15 || shapes["comparison"] < 14 {
		t.Errorf("files by shape %v", shapes)
	}
	type key struct {
		source string
		prov   Provenance
	}
	counts := map[key]int{}
	inflight := rules.NewInflight(1 << 20)
	notExpressible := 0
	for _, c := range set.Cases {
		counts[key{c.Source, c.Provenance}]++
		if strings.Contains(c.ID, "heldout") || strings.Contains(c.File, "heldout") {
			t.Errorf("%s from %s: held-out", c.ID, c.File)
		}
		if c.Ruleset == "" && c.NoRuleset == "" || c.Request == nil && c.NoRequest == "" {
			t.Errorf("%s: no ruleset or request, and no reason", c.ID)
		}
		if c.Ruleset == "" || c.Request == nil {
			continue
		}
		_, req, err := c.Build(inflight)
		var ne *NotExpressibleError
		switch {
		case errors.As(err, &ne):
			notExpressible++
		case err != nil:
			t.Errorf("%s: %v", c.ID, err)
		case req.Variables["request"].Kind() != rules.KindMap:
			t.Errorf("%s: request is %v", c.ID, req.Variables["request"].Kind())
		}
	}
	for _, k := range []key{
		{"aruma", TestAPI}, {"aruma", Emulator}, {"hidane", Emulator}, {"pyric", TestAPI}, {"pyric", Deployed},
	} {
		if counts[k] == 0 {
			t.Errorf("no cases from %s, %s", k.source, k.prov)
		}
	}
	t.Logf("%d files, %d cases %v, %d not expressible", len(set.Files), len(set.Cases), counts, notExpressible)
}

// TestGenerateMatchesRecorded checks the Go generator against the fingerprints the recorder
// kept of the rulesets it sent.
func TestGenerateMatchesRecorded(t *testing.T) {
	root, err := Root()
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(root, "production", "raw", "*.json"))
	checked := 0
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"generated"`) {
			continue
		}
		var raw struct {
			Calls []struct {
				Request struct {
					Body struct {
						Source struct {
							Files []struct {
								Content json.RawMessage `json:"content"`
							} `json:"files"`
						} `json:"source"`
					} `json:"body"`
				} `json:"request"`
			} `json:"calls"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatal(err)
		}
		for _, call := range raw.Calls {
			for _, f := range call.Request.Body.Source.Files {
				var gen struct {
					Generated *Generator `json:"generated"`
					SHA256    string     `json:"sha256"`
					Bytes     int        `json:"bytes"`
				}
				if json.Unmarshal(f.Content, &gen) != nil || gen.Generated == nil {
					continue
				}
				src, err := gen.Generated.Generate()
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256([]byte(src))
				if hex.EncodeToString(sum[:]) != gen.SHA256 || len(src) != gen.Bytes {
					t.Errorf("%+v: %d bytes, sha256 %x; recorded %d, %s", *gen.Generated, len(src), sum, gen.Bytes, gen.SHA256)
				}
				checked++
			}
		}
	}
	if checked == 0 {
		t.Fatal("no generated ruleset was checked")
	}
}

func testAPICase(request, resource string) *Case {
	c := &Case{ID: "t", Service: rules.Firestore, Provenance: TestAPI, Ruleset: "x",
		Request: &Request{}, Expected: Expected{Outcome: Allow}}
	var r rawRequest
	if err := json.Unmarshal([]byte(request), &r); err != nil {
		panic(err)
	}
	*c.Request = Request{Method: r.Method, Path: r.Path, Auth: r.Auth, Time: r.Time, Resource: r.Resource, Query: r.Query}
	if resource != "" {
		c.Resource = Field{raw: json.RawMessage(resource)}
	}
	return c
}

func build(t *testing.T, c *Case) *rules.Request {
	t.Helper()
	_, req, err := c.Build(rules.NewInflight(1 << 20))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func field(t *testing.T, v rules.Value, names ...string) rules.Value {
	t.Helper()
	for _, n := range names {
		m, ok := v.AsMap()
		if !ok {
			t.Fatalf("%s: not a map but %v", n, v.Kind())
		}
		if v, ok = m[n]; !ok {
			t.Fatalf("no field %s", n)
		}
	}
	return v
}

// TestTestAPIValues reads JSON as the Test API does (parity-exceptions.md).
func TestTestAPIValues(t *testing.T) {
	c := testAPICase(`{"method": "get", "path": "/databases/(default)/documents/c/d"}`,
		`{"data": {"two": 777001, "hundred": 777002, "negzero": 777003, "f": 1.5, "big": 9007199254740993,
		 "max": 777004, "ts": "2020-01-02T03:04:05.5Z", "off": "2026-03-04T03:00:00+03:00", "notts": "2020-01-02",
		 "p": "/databases/(default)/documents/rooms/r2", "geo": {"latitude": 10, "longitude": 20.5},
		 "notgeo": {"latitude": 1}, "list": [1, "a", null]}}`)
	c.RawNumbers = map[string]string{"777001": "2.0", "777002": "1e2", "777003": "-0", "777004": "9223372036854775807"}
	data := field(t, build(t, c).Variables["resource"], "data")
	ints := map[string]int64{"two": 2, "hundred": 100, "negzero": 0, "big": 9007199254740992, "max": math.MaxInt64}
	for name, want := range ints {
		if got, ok := field(t, data, name).AsInt(); !ok || got != want {
			t.Errorf("%s: %v (%v), want int %d", name, got, field(t, data, name).Kind(), want)
		}
	}
	if f, ok := field(t, data, "f").AsFloat(); !ok || f != 1.5 {
		t.Errorf("f: %v", field(t, data, "f").Kind())
	}
	if ts, ok := field(t, data, "ts").AsTimestamp(); !ok || !ts.Equal(time.Date(2020, 1, 2, 3, 4, 5, 5e8, time.UTC)) {
		t.Errorf("ts: %v %v", ts, field(t, data, "ts").Kind())
	}
	if ts, ok := field(t, data, "off").AsTimestamp(); !ok || !ts.Equal(time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("off: %v", ts)
	}
	if s, ok := field(t, data, "notts").AsString(); !ok || s != "2020-01-02" {
		t.Errorf("notts: %v", field(t, data, "notts").Kind())
	}
	if p, ok := field(t, data, "p").AsPath(); !ok || strings.Join(p, "/") != "databases/(default)/documents/rooms/r2" {
		t.Errorf("p: %v %v", p, field(t, data, "p").Kind())
	}
	if lat, lng, ok := field(t, data, "geo").AsLatLng(); !ok || lat != 10 || lng != 20.5 {
		t.Errorf("geo: %v", field(t, data, "geo").Kind())
	}
	if k := field(t, data, "notgeo").Kind(); k != rules.KindMap {
		t.Errorf("notgeo: %v", k)
	}
	if l, ok := field(t, data, "list").AsList(); !ok || len(l) != 3 || l[2].Kind() != rules.KindNull {
		t.Errorf("list: %v", l)
	}
}

// TestTestAPIAbsentAndNull: a field the Test API was not sent is absent from the map, and null
// is a value.
func TestTestAPIAbsentAndNull(t *testing.T) {
	absent := build(t, testAPICase(`{"method": "get", "path": "/databases/(default)/documents/c/d"}`, ""))
	req, _ := absent.Variables["request"].AsMap()
	for _, name := range []string{"auth", "time", "resource", "query"} {
		if _, ok := req[name]; ok {
			t.Errorf("request.%s is present", name)
		}
	}
	if _, ok := absent.Variables["resource"]; ok {
		t.Error("resource is present")
	}
	if p, ok := req["path"].AsPath(); !ok || len(p) != 5 {
		t.Errorf("request.path %v", p)
	}
	if m, _ := req["method"].AsString(); m != "get" {
		t.Errorf("request.method %q", m)
	}
	if absent.Method != rules.MethodGet || len(absent.Path) != 5 || absent.Project != Project || absent.Budget == nil {
		t.Errorf("request %+v", absent)
	}

	null := build(t, testAPICase(`{"method": "update", "path": "/databases/(default)/documents/c/d", "auth": null,
		"resource": null, "time": "2026-01-15T10:20:30.123Z"}`, "null"))
	req, _ = null.Variables["request"].AsMap()
	for _, name := range []string{"auth", "resource"} {
		if req[name].Kind() != rules.KindNull {
			t.Errorf("request.%s is %v, want null", name, req[name].Kind())
		}
	}
	if req["time"].Kind() != rules.KindTimestamp || null.Variables["resource"].Kind() != rules.KindNull {
		t.Errorf("time %v, resource %v", req["time"].Kind(), null.Variables["resource"].Kind())
	}

	// What the Test API refused, or takes and no real request has, is not expressible.
	for _, r := range []string{
		`{"method": "get", "path": "/databases/(default)/documents/c/d", "time": "yesterday"}`,
		`{"method": "write", "path": "/databases/(default)/documents/c/d"}`,
	} {
		var ne *NotExpressibleError
		if _, _, err := testAPICase(r, "").Build(rules.NewInflight(1 << 20)); !errors.As(err, &ne) {
			t.Errorf("%s: %v, want not expressible", r, err)
		}
	}
}

func mocked(t *testing.T, c *Case, mocks string) *MemoryDocuments {
	t.Helper()
	var raw []rawMock
	if err := json.Unmarshal([]byte(mocks), &raw); err != nil {
		t.Fatal(err)
	}
	for _, m := range raw {
		mock, err := m.mock()
		if err != nil {
			t.Fatal(err)
		}
		c.Mocks = append(c.Mocks, mock)
	}
	return build(t, c).Documents.(*MemoryDocuments)
}

const mocks = `[
 {"function": "get", "args": [{"exactValue": "/databases/(default)/documents/u/a"}], "result": {"value": {"data": {"n": 1}}}},
 {"function": "get", "args": [{"exactValue": "/databases/(default)/documents/u/null"}], "result": {"value": null}},
 {"function": "get", "args": [{"exactValue": "/databases/(default)/documents/u/undef"}], "result": {"undefined": {}}},
 {"function": "exists", "args": [{"exactValue": "/databases/(default)/documents/u/yes"}], "result": {"value": true}},
 {"function": "exists", "args": [{"exactValue": "/databases/(default)/documents/u/no"}], "result": {"value": false}}
]`

func get(t *testing.T, d rules.Documents, name string) (rules.Value, bool, error) {
	t.Helper()
	return d.Get(context.Background(), Project, []string{"databases", "(default)", "documents", "u", name}, false)
}

func TestTestAPIMocks(t *testing.T) {
	docs := mocked(t, testAPICase(`{"method": "get", "path": "/databases/(default)/documents/c/d"}`, ""), mocks)
	doc, found, err := get(t, docs, "a")
	if !found || err != nil {
		t.Fatalf("a: %v %v", found, err)
	}
	if _, ok := doc.AsMap(); !ok || len(mustMap(doc)) != 1 {
		t.Errorf("a: %v: the Test API's mocked result has data alone", mustMap(doc))
	}
	for name, want := range map[string]error{"null": nil, "no": nil, "undef": rules.ErrNoDocuments, "unmocked": rules.ErrNoDocuments} {
		if _, found, err := get(t, docs, name); found || !errors.Is(err, want) {
			t.Errorf("%s: found %v, error %v; want not found, %v", name, found, err, want)
		}
	}
	if _, found, err := get(t, docs, "yes"); !found || err != nil {
		t.Errorf("yes: %v %v", found, err)
	}
	if docs.Reads(Project) != 6 || docs.ReadsOf(Project, "/databases/(default)/documents/u/a", false) != 1 || docs.Reads(2) != 0 {
		t.Errorf("reads %d", docs.Reads(Project))
	}
}

func mustMap(v rules.Value) map[string]rules.Value {
	m, _ := v.AsMap()
	return m
}

func TestEmulatorBinding(t *testing.T) {
	c := testAPICase(`{"method": "create", "path": "/databases/(default)/documents/c/d",
		"resource": {"data": {"x": 777001, "t": "2020-01-02T03:04:05Z"}}}`, `{"data": {"n": 1, "f": 2.5}}`)
	c.Provenance = Emulator
	c.RawNumbers = map[string]string{"777001": "2.0"}
	docs := mocked(t, c, mocks)
	req := build(t, c)
	r, _ := req.Variables["request"].AsMap()
	if r["auth"].Kind() != rules.KindNull || r["time"].Kind() != rules.KindTimestamp {
		t.Errorf("auth %v, time %v", r["auth"].Kind(), r["time"].Kind())
	}
	if id, _ := field(t, r["resource"], "id").AsString(); id != "d" {
		t.Errorf("request.resource.id %q", id)
	}
	if k := field(t, r["resource"], "data", "x").Kind(); k != rules.KindFloat {
		t.Errorf("2.0 is %v on the emulator, want float", k)
	}
	if k := field(t, r["resource"], "data", "t").Kind(); k != rules.KindString {
		t.Errorf("a time string is %v on the emulator, want string", k)
	}
	if name, _ := field(t, req.Variables["resource"], "__name__").AsPath(); strings.Join(name, "/") != "databases/(default)/documents/c/d" {
		t.Errorf("resource.__name__ %v", name)
	}
	doc, found, err := get(t, docs, "a")
	if id, _ := field(t, doc, "id").AsString(); !found || err != nil || id != "a" {
		t.Errorf("a: %v %v %q", found, err, id)
	}
	for _, name := range []string{"null", "undef", "no", "unmocked"} {
		if _, found, err := get(t, docs, name); found || err != nil {
			t.Errorf("%s: found %v, error %v; want not found", name, found, err)
		}
	}

	// A signed-in user carries the ID token's claims; Storage's request has no method.
	c = testAPICase(`{"method": "get", "path": "/b/bk/o/a/b.txt", "auth": {"uid": "alice", "token": {"role": "x"}}}`,
		`{"size": 3, "timeCreated": "2026-01-02T03:04:05.000Z"}`)
	c.Service, c.Provenance = rules.Storage, Emulator
	req = build(t, c)
	r, _ = req.Variables["request"].AsMap()
	if _, ok := r["method"]; ok {
		t.Error("the Storage emulator's request has a method")
	}
	if role, _ := field(t, r["auth"], "token", "role").AsString(); role != "x" {
		t.Errorf("token.role %q", role)
	}
	if k := field(t, req.Variables["resource"], "timeCreated").Kind(); k != rules.KindTimestamp {
		t.Errorf("timeCreated is %v", k)
	}
	if len(req.Path) != 5 {
		t.Errorf("path %v", req.Path)
	}
}

func TestFirebaseRESTValues(t *testing.T) {
	c := testAPICase(`{"method": "get", "path": "/databases/(default)/documents/rooms/r1", "auth": null}`,
		`{"data": {"n": {"integerValue": "1"}, "one_f": {"doubleValue": 1}, "ts": {"timestampValue": "2020-01-02T03:04:05.123Z"},
		 "geo": {"geoPointValue": {"latitude": 10, "longitude": 20}}, "by": {"bytesValue": "AQI="},
		 "ref": {"referenceValue": "projects/rules/databases/(default)/documents/rooms/r2"},
		 "big": {"integerValue": "9223372036854775807"}, "arr": {"arrayValue": {"values": [{"stringValue": "a"}]}},
		 "m": {"mapValue": {"fields": {"x": {"nullValue": null}}}}}}`)
	c.Provenance, c.Encoding = Emulator, EncodingFirestoreREST
	data := field(t, build(t, c).Variables["resource"], "data")
	for name, want := range map[string]rules.Kind{"n": rules.KindInt, "one_f": rules.KindFloat, "ts": rules.KindTimestamp,
		"geo": rules.KindLatLng, "by": rules.KindBytes, "ref": rules.KindPath, "arr": rules.KindList, "m": rules.KindMap} {
		if k := field(t, data, name).Kind(); k != want {
			t.Errorf("%s: %v, want %v", name, k, want)
		}
	}
	if b, _ := field(t, data, "big").AsInt(); b != math.MaxInt64 {
		t.Errorf("big %d", b)
	}
	if p, _ := field(t, data, "ref").AsPath(); strings.Join(p, "/") != "databases/(default)/documents/rooms/r2" {
		t.Errorf("ref %v", p)
	}
}

func TestFirstError(t *testing.T) {
	for _, c := range []struct {
		msg       string
		line, col int
		desc      string
	}{
		{"ERROR L5:35 Invalid regular expression pattern. Pattern: a(?=b).*.", 5, 35, "Invalid regular expression pattern. Pattern: a(?=b).*."},
		{"Error compiling rules:\nL5:25 Missing a closing '\nL5:25 Unexpected 'b'.", 5, 25, "Missing a closing '"},
		{"WARNING Lundefined:undefined Ruleset uses old version.\nWARNING L4:14 Unused function: f.\nERROR L3:1 Unexpected 'x'.", 3, 1, "Unexpected 'x'."},
		{"Error compiling rules:\nL0:0 Ruleset uses old version (version [1]).\nL4:24 Let bindings are not allowed in rules_version=\"1\";",
			4, 24, "Let bindings are not allowed in rules_version=\"1\";"},
	} {
		line, col, desc, ok := Expected{Message: c.msg}.FirstError()
		if !ok || line != c.line || col != c.col || desc != c.desc {
			t.Errorf("%q: %d:%d %q %v", c.msg, line, col, desc, ok)
		}
	}
	if _, _, _, ok := (Expected{Message: "400 INVALID_ARGUMENT Total size of sources"}).FirstError(); ok {
		t.Error("a request error has a position")
	}
}

func TestExclusion(t *testing.T) {
	set := loadOracle(t)
	byID := map[string]map[Provenance]*Case{}
	for _, c := range set.Cases {
		if byID[c.ID] == nil {
			byID[c.ID] = map[Provenance]*Case{}
		}
		byID[c.ID][c.Provenance] = c
	}
	for _, c := range []struct {
		id      string
		prov    Provenance
		counted bool
	}{
		{"aruma/errors/debug_function", TestAPI, false},
		{"aruma/limits/exists_50", TestAPI, false},
		{"aruma/limits/exists_10", TestAPI, true},
		{"aruma/methods/method_write_verb", TestAPI, false},
		{"aruma/encoding/time_bad_format", TestAPI, false},
		{"aruma/limits/size_functions_2000", TestAPI, false},
		{"aruma/followups/size_comment_262120", TestAPI, true},
		{"aruma/encoding/auth_absent", TestAPI, true},
		{"aruma/limits/exists_get_same_6", Emulator, false},
		{"aruma/errors/debug_function", Emulator, true},
	} {
		k := byID[c.id][c.prov]
		if k == nil {
			t.Errorf("%s (%s) not loaded", c.id, c.prov)
			continue
		}
		if why := Exclusion(k); (why == "") != c.counted {
			t.Errorf("%s (%s): exclusion %q, counted should be %v", c.id, c.prov, why, c.counted)
		}
	}
}
