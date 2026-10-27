package acceptance

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	documents "github.com/arumadev/aruma/internal/services/documents/core"
)

// F3's firebase-tools condition (ADR-025 §4.5, ADR-028 §2), proved with firebase-tools 15.34.0
// itself (testdata/firebase-cli) against a live Aruma with the Document Database, configured
// as TestStorageWithTheFirebaseCLI configures it plus FIRESTORE_URL, through the same recording
// TLS front, network guard and sandbox (no request leaves loopback; the test fails on any
// attempt):
//
//   - firebase deploy --only firestore:indexes of a firestore.indexes.json with composite
//     indexes (collection and collection-group scope, array-contains, a vector index) and field
//     overrides (an exemption, a collection-group index, a TTL policy);
//
//   - firebase firestore:indexes exports what was deployed without loss, and deploying the export
//     back changes nothing;
//
//   - a second deploy of the same file creates and patches nothing;
//
//   - a deploy --force of a smaller file deletes the indexes and overrides it lacks;
//
//   - firebase firestore:delete -r deletes a document and its descendants (allDescendants
//     queries), and nothing beside them.
//
//     ARUMA_FIREBASE_CLI=1 go test -run TestDocumentsWithTheFirebaseCLI ./test/acceptance/
//
// test/smoke/run.sh firebase-tools-documents runs it.
func TestDocumentsWithTheFirebaseCLI(t *testing.T) {
	if os.Getenv("ARUMA_FIREBASE_CLI") == "" {
		t.Skip("set ARUMA_FIREBASE_CLI=1 to run firebase-tools against the server (Node and npm needed)")
	}
	bin := firebaseToolsBin(t)
	e := documentsEnv(t)
	owner := e.owner()
	owner.project("cli-app")
	pat := owner.pat()
	cli := newFirebaseCLI(t, bin, e.URL, pat)
	// What a developer sets besides the management origins: the Firestore origin.
	cli.env = append(cli.env, "FIRESTORE_URL="+cli.front.URL)
	db := "/v1/projects/cli-app/databases/(default)"
	proj := cli.dir("project")
	cli.write(proj, "firebase.json", `{"firestore": {"indexes": "firestore.indexes.json"}}`)

	// The first deploy: the database, the listings, then every index created and every
	// override patched (an exemption with updateMask=indexConfig, a TTL policy without).
	cli.write(proj, "firestore.indexes.json", cliIndexes)
	out := cli.ok(proj, "deploy", "--only", "firestore:indexes", "--project", "cli-app", "--non-interactive")
	if !strings.Contains(out, "Deploy complete!") {
		t.Errorf("deploy does not complete:\n%s", out)
	}
	cli.sent("the first deploy", []string{
		"GET " + db + " 200",
		"GET " + db + "/collectionGroups/-/indexes 200",
		"GET " + db + "/collectionGroups/-/fields?filter=indexConfig.usesAncestorConfig=false%20OR%20ttlConfig:* 200",
		"POST " + db + "/collectionGroups/cities/indexes 200",
		"POST " + db + "/collectionGroups/posts/indexes 200",
		"POST " + db + "/collectionGroups/docs/indexes 200",
	})
	patches := countHops(cli, "PATCH ", "/fields/")
	if patches != 3 {
		t.Errorf("the first deploy patched %d fields, want 3:\n  %s", patches, strings.Join(cli.sentHops(), "\n  "))
	}
	waitOperations(t, e, pat)

	// The export holds what was deployed, and deploying it back changes nothing.
	exported := cli.ok(proj, "firestore:indexes", "--project", "cli-app")
	exp := exportJSON(t, exported)
	if got, want := specOf(t, exp), specOf(t, []byte(cliIndexes)); !reflect.DeepEqual(got, want) {
		t.Errorf("the export differs from the deployed file:\n got %s\nwant %s\nexport:\n%s", got, want, exported)
	}
	cli.write(proj, "firestore.indexes.json", string(exp))
	cli.ok(proj, "deploy", "--only", "firestore:indexes", "--project", "cli-app", "--non-interactive")
	unchanged(t, cli, "a deploy of the export")

	// The same file again: nothing created, nothing patched.
	cli.write(proj, "firestore.indexes.json", cliIndexes)
	cli.ok(proj, "deploy", "--only", "firestore:indexes", "--project", "cli-app", "--non-interactive")
	unchanged(t, cli, "the second deploy")

	// --force with one index and no override: the rest is deleted, the overrides reverted
	// (PATCH {field.name} with {}).
	cli.write(proj, "firestore.indexes.json", cliIndexesSmall)
	out = cli.ok(proj, "deploy", "--only", "firestore:indexes", "--project", "cli-app", "--non-interactive", "--force")
	if n := countHops(cli, "DELETE ", "/indexes/"); n != 2 {
		t.Errorf("the forced deploy deleted %d indexes, want 2:\n%s", n, out)
	}
	if n := countHops(cli, "PATCH ", "/fields/"); n != 3 {
		t.Errorf("the forced deploy reverted %d fields, want 3:\n%s", n, out)
	}
	waitOperations(t, e, pat)
	if got, want := specOf(t, exportJSON(t, cli.ok(proj, "firestore:indexes", "--project", "cli-app"))), specOf(t, []byte(cliIndexesSmall)); !reflect.DeepEqual(got, want) {
		t.Errorf("after the forced deploy:\n got %s\nwant %s", got, want)
	}

	// firestore:delete -r: users/u1 and everything under it, not users/u2.
	commit := `{"writes":[` +
		`{"update":{"name":"projects/cli-app/databases/(default)/documents/users/u1","fields":{"n":{"integerValue":"1"}}}},` +
		`{"update":{"name":"projects/cli-app/databases/(default)/documents/users/u1/posts/p1","fields":{"n":{"integerValue":"2"}}}},` +
		`{"update":{"name":"projects/cli-app/databases/(default)/documents/users/u1/posts/p1/likes/l1","fields":{"n":{"integerValue":"3"}}}},` +
		`{"update":{"name":"projects/cli-app/databases/(default)/documents/users/u2","fields":{"n":{"integerValue":"4"}}}}]}`
	if code, b := documentsCall(t, e, pat, http.MethodPost, db+"/documents:commit", commit); code != 200 {
		t.Fatalf("commit: %d %s", code, b)
	}
	cli.ok(proj, "firestore:delete", "users/u1", "-r", "--force", "--project", "cli-app")
	for path, want := range map[string]int{"users/u1": 404, "users/u1/posts/p1": 404, "users/u1/posts/p1/likes/l1": 404, "users/u2": 200} {
		if code, b := documentsCall(t, e, pat, http.MethodGet, db+"/documents/"+path, ""); code != want {
			t.Errorf("after firestore:delete -r, %s: %d %s", path, code, b)
		}
	}
	if countHops(cli, "POST ", ":runQuery") == 0 {
		t.Errorf("firestore:delete -r sent no query:\n  %s", strings.Join(cli.sentHops(), "\n  "))
	}

	// firestore:rules with Storage off: the firebaserules API answers for the Document
	// Database, and the release cloud.firestore names the ruleset deployed.
	cli.write(proj, "firebase.json", `{"firestore": {"rules": "firestore.rules", "indexes": "firestore.indexes.json"}}`)
	cli.write(proj, "firestore.rules", "rules_version = '2';\nservice cloud.firestore {\n  match /databases/{database}/documents {\n"+
		"    match /pub/{d} {\n      allow read: if true;\n    }\n  }\n}\n")
	cli.ok(proj, "deploy", "--only", "firestore:rules", "--project", "cli-app", "--non-interactive")
	if code, b := documentsCall(t, e, pat, http.MethodGet, "/v1/projects/cli-app/releases/cloud.firestore", ""); code != 200 ||
		!strings.Contains(string(b), `"rulesetName":"projects/cli-app/rulesets/`) {
		t.Errorf("after deploy --only firestore:rules, the release: %d %s", code, b)
	}
}

const cliIndexes = `{
  "indexes": [
    {"collectionGroup": "cities", "queryScope": "COLLECTION", "fields": [
      {"fieldPath": "country", "order": "ASCENDING"}, {"fieldPath": "population", "order": "DESCENDING"}]},
    {"collectionGroup": "posts", "queryScope": "COLLECTION_GROUP", "fields": [
      {"fieldPath": "tags", "arrayConfig": "CONTAINS"}, {"fieldPath": "published", "order": "DESCENDING"}]},
    {"collectionGroup": "docs", "queryScope": "COLLECTION", "fields": [
      {"fieldPath": "kind", "order": "ASCENDING"}, {"fieldPath": "embedding", "vectorConfig": {"dimension": 3, "flat": {}}}]}
  ],
  "fieldOverrides": [
    {"collectionGroup": "posts", "fieldPath": "body", "indexes": []},
    {"collectionGroup": "posts", "fieldPath": "author", "indexes": [
      {"order": "ASCENDING", "queryScope": "COLLECTION"}, {"order": "ASCENDING", "queryScope": "COLLECTION_GROUP"}]},
    {"collectionGroup": "sessions", "fieldPath": "expireAt", "ttl": true, "indexes": [
      {"order": "ASCENDING", "queryScope": "COLLECTION"}, {"order": "DESCENDING", "queryScope": "COLLECTION"},
      {"arrayConfig": "CONTAINS", "queryScope": "COLLECTION"}]}
  ]
}
`

const cliIndexesSmall = `{
  "indexes": [
    {"collectionGroup": "cities", "queryScope": "COLLECTION", "fields": [
      {"fieldPath": "country", "order": "ASCENDING"}, {"fieldPath": "population", "order": "DESCENDING"}]}
  ],
  "fieldOverrides": []
}
`

// documentsEnv is a server with the Document Database, booted as App.Run boots it, on a
// loopback deployment (public_url and listen), so the owner's aruma_pat_ is accepted from the
// TLS front on loopback (ADR-029 §3 rule 7).
func documentsEnv(t *testing.T) *Env {
	t.Helper()
	e := newEnv(t, map[string]string{"ARUMA_DOCUMENTS_ENABLED": "true", "ARUMA_LISTEN": "127.0.0.1:0"})
	if err := e.App.Documents.Boot(t.Context(), documents.InstanceOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.App.Documents.Close)
	return e
}

// documentsCall sends one Document Database REST request with token, straight to the server.
func documentsCall(t *testing.T, e *Env, token, method, path, body string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, e.URL+path, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

// waitOperations waits for every index build and field update to be done, as the build job
// runs them in the background.
func waitOperations(t *testing.T, e *Env, token string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		code, b := documentsCall(t, e, token, http.MethodGet, "/v1/projects/cli-app/databases/(default)/operations", "")
		var l struct {
			Operations []struct {
				Done bool `json:"done"`
			} `json:"operations"`
		}
		if code != 200 || json.Unmarshal(b, &l) != nil {
			t.Fatalf("operations: %d %s", code, b)
		}
		if !slices.ContainsFunc(l.Operations, func(o struct {
			Done bool `json:"done"`
		}) bool {
			return !o.Done
		}) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("operations not done after 30 s: %s", b)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// countHops counts the last command's requests of method whose path holds part.
func countHops(c *firebaseCLI, method, part string) int {
	n := 0
	for _, h := range c.sentHops() {
		if strings.HasPrefix(h, method) && strings.Contains(strings.Fields(h)[1], part) {
			n++
		}
	}
	return n
}

// unchanged checks that the last deploy created, deleted and patched nothing.
func unchanged(t *testing.T, c *firebaseCLI, what string) {
	t.Helper()
	if n := countHops(c, "POST ", "/indexes") + countHops(c, "DELETE ", "/indexes/") + countHops(c, "PATCH ", "/fields/"); n != 0 {
		t.Errorf("%s changed the configuration (%d requests):\n  %s", what, n, strings.Join(c.sentHops(), "\n  "))
	}
}

// exportJSON is the JSON object firebase firestore:indexes prints.
func exportJSON(t *testing.T, out string) []byte {
	t.Helper()
	i, j := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if i < 0 || j < i || !json.Valid([]byte(out[i:j+1])) {
		t.Fatalf("firestore:indexes printed no JSON:\n%s", out)
	}
	return []byte(out[i : j+1])
}

// specOf is a firestore.indexes.json in a canonical form, to compare a deployed file with an
// export: indexes and overrides sorted, an index's __name__ and the server's density left out,
// a missing ttl false, and an override's indexes sorted.
func specOf(t *testing.T, b []byte) string {
	t.Helper()
	var f struct {
		Indexes []struct {
			CollectionGroup string           `json:"collectionGroup"`
			QueryScope      string           `json:"queryScope"`
			Fields          []map[string]any `json:"fields"`
		} `json:"indexes"`
		FieldOverrides []struct {
			CollectionGroup string           `json:"collectionGroup"`
			FieldPath       string           `json:"fieldPath"`
			TTL             bool             `json:"ttl"`
			Indexes         []map[string]any `json:"indexes"`
		} `json:"fieldOverrides"`
	}
	if err := json.NewDecoder(bytes.NewReader(b)).Decode(&f); err != nil {
		t.Fatal(err)
	}
	var ixs, ovs []string
	for _, ix := range f.Indexes {
		fields := slices.DeleteFunc(slices.Clone(ix.Fields), func(m map[string]any) bool { return m["fieldPath"] == "__name__" })
		ixs = append(ixs, string(mustJSON([]any{ix.CollectionGroup, ix.QueryScope, fields})))
	}
	for _, ov := range f.FieldOverrides {
		var modes []string
		for _, m := range ov.Indexes {
			delete(m, "density")
			modes = append(modes, string(mustJSON(m)))
		}
		slices.Sort(modes)
		ovs = append(ovs, string(mustJSON([]any{ov.CollectionGroup, ov.FieldPath, ov.TTL, modes})))
	}
	slices.Sort(ixs)
	slices.Sort(ovs)
	return string(mustJSON([]any{ixs, ovs}))
}
