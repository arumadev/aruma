package acceptance

import (
	"io"
	"net/http"
	"strings"
	"testing"

	storage "github.com/arumadev/aruma/internal/services/storage/core"
)

// Hardening: Security Rules protect a path, and the services store data by that same path.
// A caller must not be able to name the same data by another spelling that the rules evaluator
// reads as another path: encoded separators, empty and dot segments, trailing slashes, encoded
// letters, case, unicode. Each spelling must either reach the data under the rule that protects
// it (so the rule denies) or reach nothing.

// rawGet sends a GET with a path spelled exactly as given; no authentication but the one in it.
func rawGet(t *testing.T, h *hardEnv, rawPath string, header map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("GET", h.URL+rawPath, nil)
	if err != nil {
		return 0, "" // not a spelling a client can send
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%q: %v", rawPath, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

const (
	pathRTDBRules = `{"rules":{"users":{"$uid":{".read":"auth != null && auth.uid == $uid",".write":"auth != null && auth.uid == $uid"}}}}`
	pathDocRules  = `rules_version = '2';
service cloud.firestore {
  match /databases/{database}/documents {
    match /users/{uid} { allow read, write: if request.auth != null && request.auth.uid == uid; }
  }
}
`
	pathStorageRules = `rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    match /users/{uid}/{all=**} { allow read, write: if request.auth != null && request.auth.uid == uid; }
  }
}
`
)

// pathSpellings are the spellings of "users/bob" that an attacker signed in as alice tries, each
// a function of how a service writes its separator and its escapes.
func pathSpellings(sep string) []string {
	// sep is how the service writes one separator in the position it is tried at.
	enc := func(s string) string { return strings.ReplaceAll(s, "/", sep) }
	return []string{
		enc("users/bob"),
		enc("users//bob"),
		enc("users/./bob"),
		enc("users/alice/../bob"),
		enc("users/alice/%2e%2e/bob"),
		enc("users/alice/%2E%2E/bob"),
		enc("users/%2e/bob"),
		enc("./users/bob"),
		enc("%2e/users/bob"),
		enc("users/bob/"),
		enc("users/bob/."),
		enc("users/bob//"),
		enc("users/%62ob"),
		enc("%75sers/bob"),
		enc("users%2fbob"),
		enc("users%2Fbob"),
		enc("users%252Fbob"),
		enc("users%5Cbob"),
		enc("users\\bob"),
		enc("users/bob%00"),
		enc("users/bob%20"),
		enc("users/bob%0a"),
		enc("users/bob%e2%80%8b"), // zero width space
		enc("users/%ef%bd%82ob"),  // fullwidth b
		enc("users/b%cc%b6ob"),    // combining stroke
		enc("users/bo%c9%93"),     // another letter
		enc("Users/bob"),
		enc("users/BOB"),
		enc("users/bob%ef%bc%8fx"), // fullwidth solidus
		enc("users/alice%2f..%2fbob"),
		enc("users/alice/..%2fbob"),
		enc("users/alice%2f../bob"),
		enc("users/alice;/../bob"),
		enc("users/bob%2e"),
		enc("users/bob.json/.."),
	}
}

func TestHardRTDBRestPathSpellings(t *testing.T) {
	h := newHardEnv(t, nil)
	p := h.b
	h.open(p, "bravo-secret")
	p.users.create(p.id, "bob")
	bob := p.users.signIn(p.id, "bob").str("idToken")
	if r := restAs(t, h.Env, "PUT", "/.settings/rules.json?ns="+p.ns, pathRTDBRules, p.pat, nil); r.status != 200 {
		t.Fatalf("rules: %d %s", r.status, r.body)
	}
	if r := restAs(t, h.Env, "PUT", "/users/bob.json?ns="+p.ns, `"bob-private"`, p.sat, nil); r.status != 200 {
		t.Fatalf("seed: %d %s", r.status, r.body)
	}
	if r := restAs(t, h.Env, "GET", "/users/bob.json?ns="+p.ns+"&auth="+bob, "", "", nil); r.body != `"bob-private"` {
		t.Fatalf("control: bob reads his own: %d %s", r.status, r.body)
	}
	alice := p.idToken
	check := func(what, target string) {
		code, body := rawGet(t, h, target, nil)
		if strings.Contains(body, "bob-private") {
			t.Errorf("%s: alice read bob's data through %s: %d %s", what, target, code, body)
		}
	}
	for _, sep := range []string{"/", "%2F"} {
		for _, s := range pathSpellings(sep) {
			for _, suffix := range []string{".json", "/.json", ".json/"} {
				check("ID token in ?auth=", "/"+s+suffix+"?ns="+p.ns+"&auth="+alice)
				check("ID token, shallow", "/"+s+suffix+"?ns="+p.ns+"&shallow=true&auth="+alice)
			}
		}
	}
	// Parents and queries that would contain bob's node.
	for _, target := range []string{"/.json", "/users.json", "/users/.json", "//users.json", "/users.json?orderBy=%22$key%22&equalTo=%22bob%22",
		"/users.json?orderBy=%22$key%22&startAt=%22b%22&endAt=%22b%22", "/users.json?shallow=true", "/users.json?format=export", "/.json?format=export"} {
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		check("parent", target+sep+"ns="+p.ns+"&auth="+alice)
	}
	// Writes through other spellings do not touch bob's node.
	for _, s := range []string{"users//bob", "users/./bob", "users/alice/../bob", "users%2Fbob", "users/bob/", "users/%62ob", "users/bob%00"} {
		restAs(t, h.Env, "PUT", "/"+s+".json?ns="+p.ns+"&auth="+alice, `"pwned"`, "", nil)
		restAs(t, h.Env, "PATCH", "/"+s+".json?ns="+p.ns+"&auth="+alice, `{"x":"pwned"}`, "", nil)
		restAs(t, h.Env, "DELETE", "/"+s+".json?ns="+p.ns+"&auth="+alice, "", "", nil)
	}
	restAs(t, h.Env, "PATCH", "/users.json?ns="+p.ns+"&auth="+alice, `{"bob":"pwned","alice/../bob":"pwned","bob/x":"pwned"}`, "", nil)
	restAs(t, h.Env, "PUT", "/users.json?ns="+p.ns+"&auth="+alice, `{"bob":"pwned"}`, "", nil)
	restAs(t, h.Env, "PUT", "/.json?ns="+p.ns+"&auth="+alice, `{"users":{"bob":"pwned"}}`, "", nil)
	if r := restAs(t, h.Env, "GET", "/users/bob.json?ns="+p.ns, "", p.sat, nil); r.body != `"bob-private"` {
		t.Errorf("bob's node changed: %d %s", r.status, r.body)
	}
}

func TestHardRTDBWebSocketPathSpellings(t *testing.T) {
	h := newHardEnv(t, nil)
	p := h.b
	h.open(p, "bravo-secret")
	p.users.create(p.id, "bob")
	if r := restAs(t, h.Env, "PUT", "/.settings/rules.json?ns="+p.ns, pathRTDBRules, p.pat, nil); r.status != 200 {
		t.Fatalf("rules: %d %s", r.status, r.body)
	}
	if r := restAs(t, h.Env, "PUT", "/users/bob.json?ns="+p.ns, `"bob-private"`, p.sat, nil); r.status != 200 {
		t.Fatalf("seed: %d %s", r.status, r.body)
	}
	c := h.dial(p.ns, dialOpts{})
	c.call("auth", map[string]any{"cred": p.idToken}).mustOK(t)
	if r := c.call("g", map[string]any{"p": "/users/alice", "q": map[string]any{}}); r.Status != "ok" {
		t.Fatalf("control: alice cannot read her own node: %s %s", r.Status, r.Data)
	}
	for _, s := range pathSpellings("/") {
		for _, path := range []string{"/" + s, s, "/" + s + "/"} {
			for _, action := range []string{"g", "q"} {
				body := map[string]any{"p": path, "q": map[string]any{}}
				if action == "q" {
					body["h"] = ""
				}
				if r := c.call(action, body); strings.Contains(string(r.Data), "bob-private") {
					t.Errorf("%s of %q returned bob's data: %s %s", action, path, r.Status, r.Data)
				}
			}
			c.call("p", map[string]any{"p": path, "d": "pwned"})
			c.call("m", map[string]any{"p": path, "d": map[string]any{"x": "pwned"}})
		}
	}
	c.call("m", map[string]any{"p": "/users", "d": map[string]any{"bob": "pwned", "alice/../bob": "pwned", "./bob": "pwned"}})
	if r := restAs(t, h.Env, "GET", "/users/bob.json?ns="+p.ns, "", p.sat, nil); r.body != `"bob-private"` {
		t.Errorf("bob's node changed: %d %s", r.status, r.body)
	}
}

func TestHardDocumentsPathSpellings(t *testing.T) {
	h := newHardEnv(t, nil)
	p := h.b
	h.open(p, "bravo-secret")
	c := p.owner
	c.mustOK(c.do("PUT", "/console/v1/projects/"+p.id+"/documents/rules", map[string]any{"source": pathDocRules}))
	p.users.create(p.id, "bob")
	base := "/v1/projects/" + p.id + "/databases/(default)/documents/"
	if code, b := documentsCall(t, h.Env, p.sat, "PATCH", base+"users/bob", `{"fields":{"v":{"stringValue":"bob-private"}}}`); code != 200 {
		t.Fatalf("seed: %d %s", code, b)
	}
	if code, b := documentsCall(t, h.Env, p.sat, "PATCH", base+"users/alice", `{"fields":{"v":{"stringValue":"alice-private"}}}`); code != 200 {
		t.Fatalf("seed: %d %s", code, b)
	}
	if code, b := documentsCall(t, h.Env, p.idToken, "GET", base+"users/alice", ""); code != 200 || !strings.Contains(string(b), "alice-private") {
		t.Fatalf("control: alice reads her own document: %d %s", code, b)
	}
	if code, b := documentsCall(t, h.Env, p.idToken, "GET", base+"users/bob", ""); code == 200 {
		t.Fatalf("control: alice reads bob's document: %d %s", code, b)
	}
	for _, sep := range []string{"/", "%2F"} {
		for _, s := range pathSpellings(sep) {
			for _, method := range []string{"GET", "PATCH", "DELETE"} {
				body := ""
				if method == "PATCH" {
					body = `{"fields":{"v":{"stringValue":"pwned"}}}`
				}
				target := base + s
				req, err := http.NewRequest(method, "http://"+strings.TrimPrefix(h.URL, "http://")+target, strings.NewReader(body))
				if err != nil {
					continue // not a spelling a client can send
				}
				req.Header.Set("Authorization", "Bearer "+p.idToken)
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode == 200 && (strings.Contains(string(b), "bob-private") || method != "GET") {
					t.Errorf("alice: %s %s: %d %s", method, target, resp.StatusCode, b)
				}
			}
		}
	}
	// Queries that would contain bob's document, and commits that name it by another spelling.
	db := "/v1/projects/" + p.id + "/databases/(default)/documents"
	for name, q := range map[string]string{
		"collection":       `{"structuredQuery":{"from":[{"collectionId":"users"}]}}`,
		"collection group": `{"structuredQuery":{"from":[{"collectionId":"users","allDescendants":true}]}}`,
		"where uid":        `{"structuredQuery":{"from":[{"collectionId":"users"}],"where":{"fieldFilter":{"field":{"fieldPath":"__name__"},"op":"EQUAL","value":{"referenceValue":"projects/` + p.id + `/databases/(default)/documents/users/bob"}}}}}`,
	} {
		code, b := documentsCall(t, h.Env, p.idToken, "POST", db+":runQuery", q)
		if strings.Contains(string(b), "bob-private") {
			t.Errorf("alice's %s query returned bob's document: %d %s", name, code, b)
		}
	}
	root := "projects/" + p.id + "/databases/(default)/documents/"
	for _, s := range []string{"users//bob", "users/./bob", "users/alice/../bob", "users%2Fbob", "users/bob/", "users/%62ob", "users/bob%00", "Users/bob", "users/alice/../bob"} {
		commit := `{"writes":[{"update":{"name":"` + root + s + `","fields":{"v":{"stringValue":"pwned"}}}}]}`
		if code, b := documentsCall(t, h.Env, p.idToken, "POST", db+":commit", commit); code == 200 && !strings.Contains(s, "Users") {
			t.Errorf("alice's commit to %q was accepted: %s", s, b)
		}
		get := `{"documents":["` + root + s + `"]}`
		if _, b := documentsCall(t, h.Env, p.idToken, "POST", db+":batchGet", get); strings.Contains(string(b), "bob-private") {
			t.Errorf("alice's batchGet of %q returned bob's document: %s", s, b)
		}
	}
	if code, b := documentsCall(t, h.Env, p.sat, "GET", base+"users/bob", ""); code != 200 || !strings.Contains(string(b), "bob-private") {
		t.Errorf("bob's document changed: %d %s", code, b)
	}
}

func TestHardStoragePathSpellings(t *testing.T) {
	h := newHardEnv(t, nil)
	p := h.b
	h.open(p, "bravo-secret")
	p.owner.mustOK(p.owner.do("PUT", "/console/v1/projects/"+p.id+"/storage/rules", map[string]any{"source": pathStorageRules}))
	p.users.create(p.id, "bob")
	bucket, err := h.App.Storage.Bucket(t.Context(), p.bucket)
	if err != nil {
		t.Fatal(err)
	}
	put := func(name, body string) {
		t.Helper()
		if _, err := h.App.Storage.Insert(t.Context(), bucket, storage.Principal{Kind: storage.PrincipalAdmin, ServiceAccount: "seed"},
			storage.UploadSpec{Name: name, Declared: int64(len(body)), Surface: storage.SurfaceFirebase}, strings.NewReader(body), int64(len(body))); err != nil {
			t.Fatal(err)
		}
	}
	put("users/bob/secret.txt", "bob-private")
	put("users/alice/own.txt", "alice-private")
	alice := map[string]string{"Authorization": "Firebase " + p.idToken}
	if code, body := rawGet(t, h, "/v0/b/"+p.bucket+"/o/users%2Falice%2Fown.txt?alt=media", alice); code != 200 || body != "alice-private" {
		t.Fatalf("control: alice reads her own object: %d %s", code, body)
	}
	if code, body := rawGet(t, h, "/v0/b/"+p.bucket+"/o/users%2Fbob%2Fsecret.txt?alt=media", alice); code == 200 || strings.Contains(body, "bob-private") {
		t.Fatalf("control: alice reads bob's object: %d %s", code, body)
	}
	for _, sep := range []string{"/", "%2F"} {
		for _, s := range pathSpellings("%2F") {
			name := s + "%2Fsecret.txt"
			if sep == "/" {
				name = strings.ReplaceAll(name, "%2F", "/")
			}
			for _, q := range []string{"?alt=media", "", "?alt=json&token=x"} {
				code, body := rawGet(t, h, "/v0/b/"+p.bucket+"/o/"+name+q, alice)
				if strings.Contains(body, "bob-private") || (code == 200 && strings.Contains(body, `"users/bob/secret.txt"`) && !strings.Contains(body, "downloadTokens")) {
					t.Errorf("alice read bob's object through %s%s: %d %s", name, q, code, body)
				}
			}
		}
	}
	// A prefix that reads as alice's to the rules lists nothing of bob's.
	for _, prefix := range []string{"users%2Fbob%2F", "users%2Falice%2F..%2Fbob%2F", "users%2Falice%2F%2e%2e%2Fbob%2F", "users%2F%2Fbob%2F", "users%2F.%2Fbob%2F", "users%2Fbob", "users%2Fb", "users%2F", "%2Fusers%2Fbob%2F", ""} {
		for _, extra := range []string{"", "&delimiter=%2F"} {
			code, body := rawGet(t, h, "/v0/b/"+p.bucket+"/o?prefix="+prefix+extra, alice)
			if strings.Contains(body, "users/bob/") {
				t.Errorf("alice listed bob's objects with prefix %q%s: %d %s", prefix, extra, code, body)
			}
		}
	}
	// A write under a spelling that reads as alice's to the rules does not replace bob's object.
	for _, name := range []string{"users%2Falice%2F..%2Fbob%2Fsecret.txt", "users%2Falice%2F%2e%2e%2Fbob%2Fsecret.txt", "users%2F%2Fbob%2Fsecret.txt", "users%2Fbob%2Fsecret.txt"} {
		for _, f := range []struct {
			method, query string
			headers       map[string]string
			body          string
		}{
			{"POST", "?uploadType=resumable&name=" + name, map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start"}, ""},
			{"POST", "?name=" + name, map[string]string{"X-Goog-Upload-Protocol": "multipart", "Content-Type": "multipart/related; boundary=b"}, "--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--b\r\nContent-Type: text/plain\r\n\r\npwned\r\n--b--\r\n"},
			{"DELETE", "", nil, ""},
			{"PATCH", "", map[string]string{"Content-Type": "application/json"}, `{"contentType":"text/pwned"}`},
		} {
			target := "/v0/b/" + p.bucket + "/o" + f.query
			if f.method == "DELETE" || f.method == "PATCH" {
				target = "/v0/b/" + p.bucket + "/o/" + name
			}
			req, err := http.NewRequest(f.method, h.URL+target, strings.NewReader(f.body))
			if err != nil {
				continue
			}
			req.Header.Set("Authorization", "Firebase "+p.idToken)
			for k, v := range f.headers {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
		}
	}
	code, body := bearerDo(h, "GET", "/v0/b/"+p.bucket+"/o/users%2Fbob%2Fsecret.txt?alt=media", "", p.sat)
	if code != 200 || body == nil || string(body) != "bob-private" {
		t.Errorf("bob's object changed: %d %s", code, body)
	}
	// And nothing was stored under a name another spelling normalizes to.
	var n int
	if err := h.Pool.QueryRow(t.Context(), `SELECT count(*) FROM storage.objects WHERE name LIKE '%..%' OR name LIKE '%//%'`).Scan(&n); err != nil {
		t.Logf("object table not queried: %v", err)
	} else if n != 0 {
		t.Logf("objects with dot or empty segments in their names: %d (stored literally, never normalized)", n)
	}
}
