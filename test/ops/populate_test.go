package ops

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fixture is everything populate created, written to a JSON file by the upgrade test so a
// later process can check it.
type fixture struct {
	OwnerEmail    string     `json:"ownerEmail"`
	OwnerPassword string     `json:"ownerPassword"`
	PAT           string     `json:"pat"`
	Projects      []*project `json:"projects"`
	// Half is the resumable upload left half done, per project id.
	Half map[string]*halfUpload `json:"half"`
}

type halfUpload struct {
	Session string `json:"session"`
	Name    string `json:"name"`
	Sent    int    `json:"sent"`
	Total   int    `json:"total"`
}

const (
	ownerPassword = "owner-pass-0123"
	chunk         = 256 << 10
)

// det returns n bytes derived from name: the same name gives the same bytes in every process.
func det(name string, n int) []byte {
	out := make([]byte, 0, n+32)
	var ctr uint64
	for len(out) < n {
		h := sha256.Sum256(append([]byte(name), binary.BigEndian.AppendUint64(nil, ctr)...))
		out = append(out, h[:]...)
		ctr++
	}
	return out[:n]
}

// populate fills a fresh deployment with two projects through the public APIs only.
func populate(t *testing.T, d *deployment) *fixture {
	t.Helper()
	o := createOwner(t, d, "owner@example.com", ownerPassword)
	fx := &fixture{OwnerEmail: "owner@example.com", OwnerPassword: ownerPassword, PAT: o.pat, Half: map[string]*halfUpload{}}
	for _, id := range []string{"alpha-proj", "bravo-proj"} {
		p := o.newProject(id)
		fx.Projects = append(fx.Projects, p)
		populateAuth(t, o, p)
		populateRTDB(t, o, p)
		populateDocuments(t, o, p)
		fx.Half[id] = populateStorage(t, o, p)
	}
	return fx
}

func populateAuth(t *testing.T, o *owner, p *project) {
	a := o.a
	signUp := func(body map[string]any) reply {
		body["returnSecureToken"] = true
		return a.must("POST", itk+"accounts:signUp?key="+p.Key, nil, body)
	}
	alice := signUp(map[string]any{"email": "alice@" + p.ID + ".example", "password": "alice-pass-1"})
	p.Users["alice"] = &user{Email: "alice@" + p.ID + ".example", Password: "alice-pass-1", UID: alice.str("localId"), IDToken: alice.str("idToken"), Refresh: alice.str("refreshToken")}
	anon := signUp(map[string]any{})
	p.Users["anon"] = &user{UID: anon.str("localId"), IDToken: anon.str("idToken"), Refresh: anon.str("refreshToken")}

	mk := func(name string, body map[string]any) *user {
		r := o.call("POST", "/console/v1/projects/"+p.ID+"/auth/users", body)
		u := &user{Email: fmt.Sprint(body["email"]), Password: fmt.Sprint(body["password"]), UID: r.str("uid")}
		if u.UID == "" {
			u.UID = r.str("localId")
		}
		p.Users[name] = u
		return u
	}
	claims := mk("claims", map[string]any{"email": "claims@" + p.ID + ".example", "password": "claims-pass-1", "displayName": "Claims", "emailVerified": true})
	u := o.call("PATCH", "/console/v1/projects/"+p.ID+"/auth/users/"+claims.UID, map[string]any{"customClaims": `{"role":"admin","level":3}`})
	_ = u
	claims.Claims = map[string]any{"role": "admin", "level": float64(3)}
	in := a.must("POST", itk+"accounts:signInWithPassword?key="+p.Key, nil, map[string]any{"email": claims.Email, "password": claims.Password, "returnSecureToken": true})
	claims.IDToken, claims.Refresh = in.str("idToken"), in.str("refreshToken")

	dis := mk("disabled", map[string]any{"email": "disabled@" + p.ID + ".example", "password": "disabled-pass-1", "disabled": true})
	dis.Disabled = true
}

func rtdbURL(p *project, path string, q string) string {
	return "/" + strings.TrimPrefix(path, "/") + ".json?ns=" + p.NS + q
}

const rtdbRules = `{"rules":{
  "pub":{".read":true,".write":false},
  "users":{"$uid":{".read":"auth != null && auth.uid === $uid",".write":"auth != null && auth.uid === $uid"}},
  "chat":{".read":"auth != null",".write":"auth != null && auth.token.role === 'admin'"}
}}`

func populateRTDB(t *testing.T, o *owner, p *project) {
	a := o.a
	a.must("PUT", "/.settings/rules.json?ns="+p.NS, bearer(o.pat), rtdbRules)
	deep := any("leaf")
	for i := 0; i < 12; i++ {
		deep = map[string]any{fmt.Sprintf("l%d", i): deep}
	}
	data := map[string]any{
		"pub": map[string]any{
			"greeting": "hello", "n": 42, "pi": 3.14159265358979, "t": true, "f": false,
			"unicode": "héllo ✓ \U0001F600", "list": []any{1, 2, 3, "four"},
			"big":  strings.Repeat("0123456789", 6000),
			"prio": map[string]any{".priority": 7, "a": 1, "b": 2},
			"deep": deep,
		},
		"users": map[string]any{
			p.Users["alice"].UID: map[string]any{"name": "alice", "score": 10},
			p.Users["anon"].UID:  map[string]any{"name": "anon"},
		},
		"chat": map[string]any{
			"m1": map[string]any{".priority": 1.5, "text": "first", "ts": 1700000000000},
			"m2": map[string]any{".priority": "b", "text": "second"},
		},
		"private": map[string]any{"secret": "s3cret"},
		"counter": 0,
	}
	a.must("PUT", rtdbURL(p, "", ""), bearer(o.pat), data)
}

const documentsRules = `rules_version = '2';
service cloud.firestore { match /databases/{db}/documents {
  match /pub/{d} { allow read: if true; }
  match /users/{uid} { allow read, write: if request.auth != null && request.auth.uid == uid; }
}}`

func docsBase(p *project) string { return "/v1/projects/" + p.ID + "/databases/(default)" }

func populateDocuments(t *testing.T, o *owner, p *project) {
	base := docsBase(p)
	name := func(path string) string { return "projects/" + p.ID + "/databases/(default)/documents/" + path }
	str := func(s string) map[string]any { return map[string]any{"stringValue": s} }
	num := func(n int) map[string]any { return map[string]any{"integerValue": fmt.Sprint(n)} }
	writes := []any{}
	put := func(path string, fields map[string]any) {
		writes = append(writes, map[string]any{"update": map[string]any{"name": name(path), "fields": fields}})
	}
	put("cities/sf", map[string]any{"name": str("San Francisco"), "country": str("US"), "pop": num(870000),
		"tags": map[string]any{"arrayValue": map[string]any{"values": []any{str("bay"), str("fog")}}}})
	put("cities/la", map[string]any{"name": str("Los Angeles"), "country": str("US"), "pop": num(3900000)})
	put("cities/tokyo", map[string]any{"name": str("Tokyo"), "country": str("JP"), "pop": num(14000000)})
	put("cities/sf/landmarks/gg", map[string]any{"name": str("Golden Gate"), "built": map[string]any{"timestampValue": "1937-05-27T00:00:00Z"}})
	put("pub/x", map[string]any{"hello": str("world")})
	put("priv/x", map[string]any{"secret": str("s3cret")})
	put("users/"+p.Users["alice"].UID, map[string]any{"name": str("alice")})
	put("types/all", map[string]any{
		"null":      map[string]any{"nullValue": nil},
		"bool":      map[string]any{"booleanValue": true},
		"int":       num(-9007199254740993),
		"double":    map[string]any{"doubleValue": 2.718281828459045},
		"nan":       map[string]any{"doubleValue": "NaN"},
		"time":      map[string]any{"timestampValue": "2024-02-29T23:59:59.123456Z"},
		"string":    str("ünïcöde \U0001F600"),
		"bytes":     map[string]any{"bytesValue": "AAECAwQFBgcICQ=="},
		"ref":       map[string]any{"referenceValue": name("cities/sf")},
		"geo":       map[string]any{"geoPointValue": map[string]any{"latitude": 37.7749, "longitude": -122.4194}},
		"array":     map[string]any{"arrayValue": map[string]any{"values": []any{num(1), str("two"), map[string]any{"mapValue": map[string]any{"fields": map[string]any{"k": num(3)}}}}}},
		"map":       map[string]any{"mapValue": map[string]any{"fields": map[string]any{"nested": map[string]any{"mapValue": map[string]any{"fields": map[string]any{"deep": str("yes")}}}}}},
		"emptyMap":  map[string]any{"mapValue": map[string]any{}},
		"emptyList": map[string]any{"arrayValue": map[string]any{}},
		"long":      str(strings.Repeat("abcdefghij", 20000)),
	})
	c := o.a.must("POST", base+"/documents:commit", bearer(o.pat), map[string]any{"writes": writes})
	if c.str("commitTime") == "" {
		t.Fatalf("commit: %s", c)
	}
	// A delete leaves its mark in the version history only.
	o.a.must("PATCH", base+"/documents/cities/gone", bearer(o.pat), map[string]any{"fields": map[string]any{"x": num(1)}})
	o.a.must("DELETE", base+"/documents/cities/gone", bearer(o.pat), nil)

	// Composite indexes: one for the query below, one collection-group index with an array field.
	for _, spec := range []struct {
		group string
		body  map[string]any
	}{
		{"cities", map[string]any{"queryScope": "COLLECTION", "fields": []any{
			map[string]any{"fieldPath": "country", "order": "ASCENDING"}, map[string]any{"fieldPath": "pop", "order": "DESCENDING"}}}},
		{"landmarks", map[string]any{"queryScope": "COLLECTION_GROUP", "fields": []any{
			map[string]any{"fieldPath": "name", "order": "ASCENDING"}, map[string]any{"fieldPath": "built", "order": "DESCENDING"}}}},
	} {
		o.a.must("POST", base+"/collectionGroups/"+spec.group+"/indexes", bearer(o.pat), spec.body)
	}
	waitIndexes(t, o, p)

	// Rules: a ruleset and its release, as `firebase deploy --only firestore:rules` makes them.
	rs := o.a.must("POST", "/v1/projects/"+p.ID+"/rulesets", bearer(o.pat), map[string]any{"source": map[string]any{
		"files": []any{map[string]any{"name": "firestore.rules", "content": documentsRules}}}})
	o.a.must("POST", "/v1/projects/"+p.ID+"/releases", bearer(o.pat), map[string]any{
		"name": "projects/" + p.ID + "/releases/cloud.firestore", "rulesetName": rs.str("name")})
}

func waitIndexes(t *testing.T, o *owner, p *project) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		r := o.a.must("GET", docsBase(p)+"/collectionGroups/-/indexes", bearer(o.pat), nil)
		ixs, _ := r.obj()["indexes"].([]any)
		ready := len(ixs) > 0
		for _, x := range ixs {
			if m, _ := x.(map[string]any); m["state"] != "READY" {
				ready = false
			}
		}
		if ready {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("indexes of %s not ready: %s", p.ID, r)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

const storageRules = `rules_version = '2';
service firebase.storage { match /b/{bucket}/o {
  match /pub/{all=**} { allow read: if true; }
  match /users/{uid}/{all=**} { allow read, write: if request.auth != null && request.auth.uid == uid; }
}}`

// objSpec is one object populate stores.
type objSpec struct {
	name, ctype string
	size        int
	meta        map[string]string
}

func startUpload(t *testing.T, a *api, p *project, tok, name, ctype string, meta map[string]string) string {
	t.Helper()
	h := bearer(tok)
	h["X-Goog-Upload-Protocol"] = "resumable"
	h["X-Goog-Upload-Command"] = "start"
	body := map[string]any{"contentType": ctype}
	if meta != nil {
		body["metadata"] = meta
	}
	r := a.must("POST", "/v0/b/"+p.Bucket+"/o?uploadType=resumable&name="+url.QueryEscape(name), h, body)
	sess := r.Header.Get("X-Goog-Upload-Url")
	if sess == "" {
		t.Fatalf("no upload url: %s %v", r, r.Header)
	}
	return sess
}

func sendChunk(t *testing.T, a *api, tok, sess string, offset int, data []byte, final bool) reply {
	t.Helper()
	h := bearer(tok)
	h["X-Goog-Upload-Protocol"] = "resumable"
	h["X-Goog-Upload-Command"] = "upload"
	if final {
		h["X-Goog-Upload-Command"] = "upload, finalize"
	}
	h["X-Goog-Upload-Offset"] = fmt.Sprint(offset)
	return a.must("POST", sess, h, data)
}

// uploadObject stores one object through the resumable protocol, in chunks.
func uploadObject(t *testing.T, a *api, p *project, tok string, s objSpec) {
	t.Helper()
	data := det(p.ID+"/"+s.name, s.size)
	sess := startUpload(t, a, p, tok, s.name, s.ctype, s.meta)
	step := 4 << 20
	for off := 0; ; off += step {
		end := min(off+step, len(data))
		final := end == len(data)
		sendChunk(t, a, tok, sess, off, data[off:end], final)
		if final {
			return
		}
	}
}

func populateStorage(t *testing.T, o *owner, p *project) *halfUpload {
	a := o.a
	tok := p.token(t, a)
	rs := a.must("POST", "/v1/projects/"+p.ID+"/rulesets", bearer(o.pat), map[string]any{"source": map[string]any{
		"files": []any{map[string]any{"name": "storage.rules", "content": storageRules}}}})
	a.must("POST", "/v1/projects/"+p.ID+"/releases", bearer(o.pat), map[string]any{
		"name": "projects/" + p.ID + "/releases/firebase.storage/" + p.Bucket, "rulesetName": rs.str("name")})

	specs := []objSpec{
		{"pub/small.txt", "text/plain", 5, map[string]string{"k": "v", "owner": "alice"}},
		{"pub/sp ace/ünicöde ✓.txt", "text/plain; charset=utf-8", 1234, nil},
		{"pub/empty", "application/octet-stream", 0, nil},
		{"data/multi.bin", "application/octet-stream", 12<<20 + 12345, map[string]string{"kind": "multi"}},
		{"data/exact.bin", "application/octet-stream", 2 * chunk, nil},
		{"users/" + p.Users["alice"].UID + "/avatar.png", "image/png", 70000, nil},
	}
	for _, s := range specs {
		uploadObject(t, a, p, tok, s)
	}
	// A second generation of one name, and a deleted object: both leave a file for the sweeper.
	uploadObject(t, a, p, tok, objSpec{"pub/overwritten.txt", "text/plain", 100, nil})
	uploadObject(t, a, p, tok, objSpec{"pub/overwritten.txt", "text/plain", 200, map[string]string{"gen": "2"}})
	uploadObject(t, a, p, tok, objSpec{"pub/deleted.txt", "text/plain", 50, nil})
	a.must("DELETE", "/v0/b/"+p.Bucket+"/o/"+url.PathEscape("pub/deleted.txt"), bearer(tok), nil)

	// The upload left half done: two chunks of a 1 MiB file sent, the rest never.
	half := &halfUpload{Name: "data/half.bin", Sent: 2 * chunk, Total: 4 * chunk}
	half.Session = startUpload(t, a, p, tok, half.Name, "application/octet-stream", map[string]string{"half": "yes"})
	data := det(p.ID+"/"+half.Name, half.Total)
	sendChunk(t, a, tok, half.Session, 0, data[:half.Sent], false)
	return half
}

func (fx *fixture) save(t *testing.T) []byte {
	b, err := json.MarshalIndent(fx, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return b
}
