package acceptance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/rules"
	storage "github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire/wiretest"
)

// S3-A's acceptance cases: every /v0 operation but the uploads and downloads (S3-B, S3-C), on a
// real server and Postgres, as the three client SDKs call them (A §3, §7), every reply free of
// null. The objects are put through core.Insert as an Admin, the GCS way (no download token),
// until S3-B serves the uploads.

// v0Rules opens public/ to everyone and users/<uid>/ to its user alone.
const v0Rules = `rules_version = '2';
service firebase.storage {
  match /b/{bucket}/o {
    match /public/{all=**} { allow read, write: if true; }
    match /users/{uid}/{all=**} { allow read, write: if request.auth != null && request.auth.uid == uid; }
  }
}
`

type v0Env struct {
	*Env
	bucket storage.Bucket
	// The credentials: two users' ID tokens, the project's service account, the owner's CLI
	// token, and a service account of another project of the same owner.
	alice, bob, sa, pat, otherSA string
}

func newV0Env(t *testing.T) *v0Env {
	t.Helper()
	e := newEnv(t, map[string]string{"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": t.TempDir()})
	if e.App.Storage == nil {
		t.Fatal("Storage is not enabled")
	}
	ctx := context.Background()
	owner := e.owner()
	name := owner.project("chat-app").str("storageBucket")
	owner.project("other-app")
	b, err := e.App.Storage.Bucket(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	ve := &v0Env{Env: e, bucket: b, pat: owner.pat()}

	var account uuid.UUID
	if err := e.Pool.QueryRow(ctx, `SELECT id FROM control.accounts WHERE email = 'owner@example.com'`).Scan(&account); err != nil {
		t.Fatal(err)
	}
	p := &authn.Principal{Kind: authn.KindAccount, AccountID: account}
	rs, _, err := e.App.Service.CreateRuleset(ctx, p, "chat-app", rules.Storage, []rules.File{{Name: "storage.rules", Content: v0Rules}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.App.Service.PublishRelease(ctx, p, "chat-app", "firebase.storage/"+name, rs.Name); err != nil {
		t.Fatal(err)
	}

	users := e.rtdbUsers(owner)
	users.create("chat-app", "alice")
	users.create("chat-app", "bob")
	ve.alice = users.signIn("chat-app", "alice").str("idToken")
	ve.bob = users.signIn("chat-app", "bob").str("idToken")
	for project, dst := range map[string]*string{"chat-app": &ve.sa, "other-app": &ve.otherSA} {
		_, key := owner.serviceAccountKey(project, "storage-admin")
		if *dst, _, err = exchange(t, key); err != nil {
			t.Fatal(err)
		}
	}
	return ve
}

// put stores an object as an Admin over GCS would: no download token.
func (e *v0Env) put(name, body, contentType string) storage.Object {
	e.t.Helper()
	spec := storage.UploadSpec{Name: name, Declared: int64(len(body)), Surface: storage.SurfaceGCS}
	if contentType != "" {
		spec.Attrs.Fields = map[string]*string{storage.FieldContentType: &contentType}
	}
	o, err := e.App.Storage.Insert(context.Background(), e.bucket, storage.Principal{Kind: storage.PrincipalAdmin}, spec,
		strings.NewReader(body), int64(len(body)))
	if err != nil {
		e.t.Fatal(err)
	}
	return o
}

// v0Reply is one /v0 reply.
type v0Reply struct {
	status int
	header http.Header
	body   []byte
	json   map[string]any
}

func (r v0Reply) str(key string) string { s, _ := r.json[key].(string); return s }

// call sends one request to /v0; auth is the Authorization header ("" for none). Every reply is
// checked for null, and for the API's open CORS unless it is a download's.
func (e *v0Env) call(method, path, auth, body string, header map[string]string) v0Reply {
	e.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.URL+path, rd)
	if err != nil {
		e.t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatal(err)
	}
	r := v0Reply{status: resp.StatusCode, header: resp.Header, body: b}
	wiretest.AssertNoNull(e.t, b)
	if len(b) > 0 {
		if err := json.Unmarshal(b, &r.json); err != nil {
			e.t.Errorf("%s %s: not a JSON object: %s", method, path, b)
		}
	}
	if !strings.Contains(path, "alt=media") && resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		e.t.Errorf("%s %s: Access-Control-Allow-Origin %q", method, path, resp.Header.Get("Access-Control-Allow-Origin"))
	}
	return r
}

func (e *v0Env) obj(name string) string { return "/v0/b/" + e.bucket.Name + "/o/" + escapeName(name) }

func escapeName(name string) string {
	return strings.ReplaceAll(strings.ReplaceAll(name, "/", "%2F"), " ", "%20")
}

func (e *v0Env) expect(r v0Reply, status int, what string) {
	e.t.Helper()
	if r.status != status {
		e.t.Fatalf("%s: %d, want %d: %s", what, r.status, status, r.body)
	}
	if status >= 400 {
		code, _ := r.json["error"].(map[string]any)["code"].(float64)
		if int(code) != status {
			e.t.Fatalf("%s: not /v0's error shape: %s", what, r.body)
		}
	}
}

func firebase(token string) string { return "Firebase " + token }
func bearer(token string) string   { return "Bearer " + token }

var v0Time = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`)

// getMetadata (A §3.2, §4): the resource's shape, the token minted on the first authorized read
// of an object an Admin uploaded and kept after, the rules on the read, and who may read.
func TestStorageV0Metadata(t *testing.T) {
	e := newV0Env(t)
	o := e.put("public/a b.txt", "hello", "text/plain")

	r := e.call("GET", e.obj("public/a b.txt"), "", "", map[string]string{
		"X-Firebase-AppCheck": "not-a-token", "X-Firebase-GMPID": "1:1:web:1"}) // ignored (ADR-022 §19)
	e.expect(r, 200, "anonymous read of public/")
	for k, want := range map[string]string{"name": "public/a b.txt", "bucket": e.bucket.Name, "contentType": "text/plain",
		"size": "5", "metageneration": "2", "contentEncoding": "identity", "storageClass": "STANDARD",
		"md5Hash": "XUFAKrxLKna5cZ2REBfFkg==", "crc32c": "mnG7TA=="} {
		if got := r.str(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if r.str("generation") != itoa(int(o.Generation)) {
		t.Errorf("generation %q, want %d", r.str("generation"), o.Generation)
	}
	for _, k := range []string{"timeCreated", "updated"} {
		if !v0Time.MatchString(r.str(k)) {
			t.Errorf("%s = %q, not three fractional digits and Z", k, r.str(k))
		}
	}
	token := r.str("downloadTokens")
	if _, err := uuid.Parse(token); err != nil {
		t.Errorf("downloadTokens %q: not a minted UUID", token)
	}
	if strings.Contains(string(r.body), "firebaseStorageDownloadTokens") {
		t.Errorf("the tokens' key shows on /v0: %s", r.body)
	}
	if r.header.Get("Content-Type") != "application/json; charset=UTF-8" || r.header.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers: %v", r.header)
	}
	// The mint happens once; the prefixed form serves the same.
	r = e.call("GET", "/firebasestorage.googleapis.com"+e.obj("public/a b.txt"), "", "", nil)
	e.expect(r, 200, "second read")
	if r.str("downloadTokens") != token || r.str("metageneration") != "2" {
		t.Errorf("the second read minted again: %s", r.body)
	}

	e.put("users/alice/p.png", "png", "image/png")
	for _, c := range []struct {
		what, auth string
		status     int
	}{
		{"alice reads hers", firebase(e.alice), 200},
		{"bob reads alice's", firebase(e.bob), 403},
		{"anonymous reads alice's", "", 403},
		{"the project's service account bypasses the rules", bearer(e.sa), 200},
		{"the owner's CLI token bypasses the rules", bearer(e.pat), 200},
		{"another project's service account", bearer(e.otherSA), 401},
		{"a forged ID token", firebase(e.alice[:len(e.alice)-4] + "AAAA"), 401},
		{"a bearer that is an ID token", bearer(e.alice), 401},
		{"another scheme", "Basic YTpi", 401},
	} {
		r := e.call("GET", e.obj("users/alice/p.png"), c.auth, "", nil)
		e.expect(r, c.status, c.what)
		if c.status == 403 && r.json["error"].(map[string]any)["message"] != "Permission denied." {
			t.Errorf("%s: %s", c.what, r.body)
		}
	}
	// The rules before existence: an allowed read of nothing is 404, a refused one 403.
	e.expect(e.call("GET", e.obj("users/alice/none"), firebase(e.alice), "", nil), 404, "alice reads a missing object")
	e.expect(e.call("GET", e.obj("users/alice/none"), firebase(e.bob), "", nil), 403, "bob reads a missing object")
	// The token opens the bytes alone, never the metadata (ADR-023 §3-18).
	e.expect(e.call("GET", e.obj("users/alice/p.png")+"?token="+token, "", "", nil), 403, "a token on the metadata")
}

// updateMetadata (A §3.5): JS's PATCH, Android's PUT override, iOS's nulls, the tokens the
// client cannot touch, metadata: null, and the rules on the change.
func TestStorageV0UpdateMetadata(t *testing.T) {
	e := newV0Env(t)
	e.put("users/alice/m.txt", "m", "text/plain")
	path := e.obj("users/alice/m.txt")
	first := e.call("GET", path, firebase(e.alice), "", nil)
	e.expect(first, 200, "read")
	token := first.str("downloadTokens")

	r := e.call("PATCH", path, firebase(e.alice), `{"cacheControl":"no-cache","contentType":"text/markdown",`+
		`"metadata":{"keep":"1","drop":"2","firebaseStorageDownloadTokens":"planted"},"downloadTokens":"mine","bucket":"","size":9}`,
		map[string]string{"Content-Type": "application/json; charset=utf-8"})
	e.expect(r, 200, "JS PATCH")
	md, _ := r.json["metadata"].(map[string]any)
	if r.str("cacheControl") != "no-cache" || r.str("contentType") != "text/markdown" || r.str("size") != "1" ||
		r.str("metageneration") != "3" || r.str("downloadTokens") != token || len(md) != 2 || md["keep"] != "1" {
		t.Errorf("after PATCH: %s", r.body)
	}
	// Android: PUT with the override; null deletes a key and a field.
	r = e.call("PUT", path, firebase(e.alice), `{"metadata":{"drop":null},"cacheControl":null}`,
		map[string]string{"X-HTTP-Method-Override": "PATCH", "Content-Type": "application/json"})
	e.expect(r, 200, "Android PATCH")
	md, _ = r.json["metadata"].(map[string]any)
	if _, ok := r.json["cacheControl"]; ok || len(md) != 1 || md["keep"] != "1" {
		t.Errorf("after the override: %s", r.body)
	}
	// iOS sends null for what it read back and does not know: ignored, the tokens stay.
	r = e.call("PATCH", path, firebase(e.alice), `{"downloadTokens":null,"etag":null,"crc32c":null,"storageClass":null,`+
		`"generation":null,"timeCreated":null,"md5Hash":null,"contentLanguage":"en"}`, nil)
	e.expect(r, 200, "iOS PATCH")
	if r.str("downloadTokens") != token || r.str("contentLanguage") != "en" || r.str("md5Hash") == "" {
		t.Errorf("after iOS's nulls: %s", r.body)
	}
	// metadata: null clears the custom metadata, and the token stays (ADR-023 §3-17).
	r = e.call("PATCH", path, firebase(e.alice), `{"metadata":null}`, nil)
	e.expect(r, 200, "metadata: null")
	if _, ok := r.json["metadata"]; ok || r.str("downloadTokens") != token {
		t.Errorf("after metadata: null: %s", r.body)
	}

	e.expect(e.call("PATCH", path, firebase(e.bob), `{"contentType":"x/y"}`, nil), 403, "bob's PATCH")
	e.expect(e.call("PATCH", path, "", `{"contentType":"x/y"}`, nil), 403, "an anonymous PATCH")
	e.expect(e.call("PATCH", e.obj("users/alice/none"), firebase(e.alice), `{}`, nil), 404, "PATCH of nothing")
	e.expect(e.call("PATCH", path, firebase(e.alice), `{"contentType":5}`, nil), 400, "a number for a field")
	e.expect(e.call("PATCH", path, firebase(e.alice), `not json`, nil), 400, "not JSON")
	e.expect(e.call("PATCH", path, firebase(e.alice), `{"contentType":"a\nb"}`, nil), 400, "a header field with LF")
}

// delete (A §3.6): 204 without a body, then 404; the rules decide first.
func TestStorageV0Delete(t *testing.T) {
	e := newV0Env(t)
	e.put("users/alice/d.txt", "d", "")
	path := e.obj("users/alice/d.txt")
	e.expect(e.call("DELETE", path, firebase(e.bob), "", nil), 403, "bob's delete")
	r := e.call("DELETE", path, firebase(e.alice), "", nil)
	e.expect(r, 204, "alice's delete")
	if len(r.body) != 0 {
		t.Errorf("a body on 204: %s", r.body)
	}
	e.expect(e.call("GET", path, firebase(e.alice), "", nil), 404, "read after the delete")
	e.expect(e.call("DELETE", path, firebase(e.alice), "", nil), 404, "delete again")
	e.put("users/alice/d2.txt", "d", "")
	e.expect(e.call("DELETE", e.obj("users/alice/d2.txt"), bearer(e.sa), "", nil), 204, "the service account's delete")
}

// create_token and delete_token (E §5, CT): admins only, create_token=true alone, the last
// token's deletion making a new one.
func TestStorageV0Tokens(t *testing.T) {
	e := newV0Env(t)
	e.put("public/t.txt", "t", "")
	path := e.obj("public/t.txt")
	first := e.call("GET", path, "", "", nil).str("downloadTokens")

	e.expect(e.call("POST", path+"?create_token=true", firebase(e.alice), "", nil), 403, "a user's create_token")
	e.expect(e.call("POST", path+"?create_token=true", "", "", nil), 403, "an anonymous create_token")
	e.expect(e.call("POST", path+"?create_token=false", firebase(e.alice), "", nil), 400, "create_token=false")
	e.expect(e.call("POST", path+"?delete_token="+first, firebase(e.alice), "", nil), 403, "a user's delete_token")

	r := e.call("POST", path+"?create_token=true", bearer(e.sa), "", nil)
	e.expect(r, 200, "the admin's create_token")
	tokens := strings.Split(r.str("downloadTokens"), ",")
	if len(tokens) != 2 || tokens[0] != first {
		t.Fatalf("after create_token: %q", tokens)
	}
	r = e.call("POST", path+"?delete_token="+first, bearer(e.pat), "", nil)
	e.expect(r, 200, "the owner's delete_token")
	if r.str("downloadTokens") != tokens[1] {
		t.Fatalf("after delete_token: %s", r.body)
	}
	r = e.call("POST", path+"?delete_token="+tokens[1], bearer(e.sa), "", nil)
	e.expect(r, 200, "deleting the last token")
	if got := r.str("downloadTokens"); got == "" || got == tokens[1] || strings.Contains(got, ",") {
		t.Fatalf("the last token's deletion left %q", got)
	}
	e.expect(e.call("POST", e.obj("public/none")+"?create_token=true", bearer(e.sa), "", nil), 404, "create_token on nothing")
}

// list and listAll (A §3.7, ADR-022 §13): prefixes with their slash, items of two strings, the
// pages walked to the end without an empty nextPageToken, the empty folder's two arrays.
func TestStorageV0List(t *testing.T) {
	e := newV0Env(t)
	for _, n := range []string{"public/l/a", "public/l/b", "public/l/d/c", "public/l/d/e", "public/l/f/g", "public/l//x", "public/m"} {
		e.put(n, "x", "")
	}
	list := "/v0/b/" + e.bucket.Name + "/o"
	r := e.call("GET", list+"?prefix=public%2Fl%2F&delimiter=%2F", "", "", nil)
	e.expect(r, 200, "list")
	if string(r.body) != `{"prefixes":["public/l/d/","public/l/f/"],"items":[{"name":"public/l/a","bucket":"`+e.bucket.Name+
		`"},{"name":"public/l/b","bucket":"`+e.bucket.Name+`"}]}` {
		t.Errorf("list: %s", r.body)
	}
	// listAll's pages of one, to the end.
	var prefixes, items []string
	token := ""
	for page := 0; ; page++ {
		if page > 10 {
			t.Fatal("the pages do not end")
		}
		q := list + "?prefix=public%2Fl%2F&delimiter=%2F&maxResults=1"
		if token != "" {
			q += "&pageToken=" + token
		}
		r := e.call("GET", q, "", "", nil)
		e.expect(r, 200, "a page")
		for _, p := range r.json["prefixes"].([]any) {
			prefixes = append(prefixes, p.(string))
		}
		for _, it := range r.json["items"].([]any) {
			items = append(items, it.(map[string]any)["name"].(string))
		}
		raw, ok := r.json["nextPageToken"]
		if !ok {
			break
		}
		if token, _ = raw.(string); token == "" {
			t.Fatalf("an empty nextPageToken: %s", r.body)
		}
	}
	if !slices.Equal(prefixes, []string{"public/l/d/", "public/l/f/"}) || !slices.Equal(items, []string{"public/l/a", "public/l/b"}) {
		t.Errorf("the pages: %q %q", prefixes, items)
	}
	r = e.call("GET", list+"?prefix=public%2Fnone%2F&delimiter=%2F", "", "", nil)
	e.expect(r, 200, "an empty folder")
	if string(r.body) != `{"prefixes":[],"items":[]}` {
		t.Errorf("an empty folder: %s", r.body)
	}
	e.expect(e.call("GET", list+"?prefix=users%2Falice%2F&delimiter=%2F", firebase(e.bob), "", nil), 403, "bob lists alice's")
	e.expect(e.call("GET", list+"?prefix=users%2Falice%2F&delimiter=%2F", firebase(e.alice), "", nil), 200, "alice lists hers")
	e.expect(e.call("GET", list+"?prefix=public%2F&pageToken=forged", "", "", nil), 400, "a forged page token")
	e.expect(e.call("GET", list+"?prefix=public%2F&maxResults=0", "", "", nil), 400, "maxResults=0")
	e.expect(e.call("GET", list+"?prefix=public%2F&maxResults=x", "", "", nil), 400, "maxResults=x")
}

// The router's own answers on a real server (bucket_probe.json): 400 for a path that names
// nothing, 404 for an unknown bucket whatever the credential, the preflight, and CORS on
// alt=media from the bucket's rules alone.
func TestStorageV0Routing(t *testing.T) {
	e := newV0Env(t)
	for _, c := range []struct {
		method, path, auth string
		status             int
		message            string
	}{
		{"GET", "/v0/b/", "", 400, "Invalid HTTP method/URL pair."},
		{"GET", "/v0/nothing-here", "", 400, "Invalid HTTP method/URL pair."},
		{"GET", "/firebasestorage.googleapis.com/v0/b/", "", 400, "Invalid HTTP method/URL pair."},
		{"GET", "/v0/b/zz-no-such-bucket.appspot.com/o?maxResults=1", "", 404, "Not Found."},
		{"GET", "/v0/b/zz-no-such-bucket.appspot.com/o/x", "Firebase not-a-token", 404, "Not Found."},
		{"GET", "/v0/b/other-app." + e.App.Config.ProjectDomain + "/o/x", bearer(e.sa), 401, "Invalid authentication credentials."},
		{"OPTIONS", "/v0/b/zz-no-such-bucket.appspot.com/o?name=x", "", 404, "Not Found."},
	} {
		r := e.call(c.method, c.path, c.auth, "", nil)
		e.expect(r, c.status, c.method+" "+c.path)
		if r.json["error"].(map[string]any)["message"] != c.message {
			t.Errorf("%s %s: %s", c.method, c.path, r.body)
		}
	}
	r := e.call("OPTIONS", "/v0/b/"+e.bucket.Name+"/o?name=x", "", "", map[string]string{
		"Origin": "https://app.example", "Access-Control-Request-Method": "POST",
		"Access-Control-Request-Headers": "authorization,x-goog-upload-protocol"})
	e.expect(r, 204, "the API's preflight")
	if !strings.Contains(r.header.Get("Access-Control-Allow-Headers"), "X-Goog-Upload-Protocol") {
		t.Errorf("preflight: %v", r.header)
	}

	// alt=media: no CORS until the owner sets a rule, then the rule's origin alone.
	e.put("public/img.png", "png", "image/png")
	media := e.obj("public/img.png") + "?alt=media"
	preflight := func(origin string) http.Header {
		return e.call("OPTIONS", media, "", "", map[string]string{"Origin": origin, "Access-Control-Request-Method": "GET"}).header
	}
	if h := preflight("https://app.example"); h.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("a download's preflight without a CORS rule: %v", h)
	}
	if _, err := e.App.Storage.SetCORS(context.Background(), e.bucket, storage.Principal{Kind: storage.PrincipalAdmin},
		[]storage.CORSRule{{Origins: []string{"https://app.example"}, Methods: []string{"GET"}, ResponseHeaders: []string{"Content-Type"}, MaxAgeSeconds: 60}}); err != nil {
		t.Fatal(err)
	}
	h := preflight("https://app.example")
	if h.Get("Access-Control-Allow-Origin") != "https://app.example" || h.Get("Access-Control-Allow-Methods") != "GET" ||
		h.Get("Access-Control-Max-Age") != "60" {
		t.Errorf("a download's preflight under a rule: %v", h)
	}
	if h := preflight("https://evil.example"); h.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("another origin's preflight: %v", h)
	}

	// Firebase's documented cors.json, which names no responseHeader, and the preflight of the web
	// SDK's getBlob, which always sends X-Firebase-Storage-Version: the headers asked for are
	// allowed, as GCS's JSON API echoes them.
	if _, err := e.App.Storage.SetCORS(context.Background(), e.bucket, storage.Principal{Kind: storage.PrincipalAdmin},
		[]storage.CORSRule{{Origins: []string{"*"}, Methods: []string{"GET"}, MaxAgeSeconds: 3600}}); err != nil {
		t.Fatal(err)
	}
	h = e.call("OPTIONS", media, "", "", map[string]string{"Origin": "https://app.example",
		"Access-Control-Request-Method": "GET", "Access-Control-Request-Headers": "authorization,x-firebase-storage-version"}).header
	if h.Get("Access-Control-Allow-Origin") != "https://app.example" || h.Get("Access-Control-Allow-Methods") != "GET" ||
		h.Get("Access-Control-Allow-Headers") != "authorization,x-firebase-storage-version" ||
		h.Get("Access-Control-Max-Age") != "3600" || h.Get("Access-Control-Expose-Headers") != "" {
		t.Errorf("the documented cors.json's preflight: %v", h)
	}
}
