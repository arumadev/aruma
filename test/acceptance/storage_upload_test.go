package acceptance

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/rules"
	storage "github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire/wiretest"
)

// S3-B's acceptance cases: the Firebase client upload protocol (A §3.8-3.13, §7; ADR-022 §6.1,
// §6.4, §7) on a real server and Postgres. The web SDK's requests are replayed from the emulator
// recording (sdk_upload.json); Android's and iOS's have no recording, so their shapes are the
// ones A §3.10-3.13 reads from the SDKs' sources (methods, headers, bodies). Where the emulator
// is not production, production's rules are checked instead (raw_resumable.json against
// ADR-022 §6.1): offsets, the declared size, md5Hash, final on every terminal refusal.

// newUploadEnv is newV0Env's project (v0Rules: public/ open, users/<uid>/ its user's) with the
// settings env on top, and the users alice and bob.
func newUploadEnv(t *testing.T, env map[string]string) *v0Env {
	t.Helper()
	e, owner := newUploadProject(t, env)
	users := e.rtdbUsers(owner)
	users.create("chat-app", "alice")
	users.create("chat-app", "bob")
	e.alice, e.bob = users.signIn("chat-app", "alice").str("idToken"), users.signIn("chat-app", "bob").str("idToken")
	return e
}

// newUploadProject is newUploadEnv without the users, whose sign-in needs the server reachable at
// its public URL; and the project's owner.
func newUploadProject(t *testing.T, env map[string]string) (*v0Env, *Client) {
	t.Helper()
	vars := map[string]string{"ARUMA_STORAGE_ENABLED": "true", "ARUMA_STORAGE_DATA_DIR": t.TempDir(),
		"ARUMA_STORAGE_MIN_FREE_BYTES": strconv.Itoa(1 << 20)}
	for k, v := range env {
		vars[k] = v
	}
	e := newEnv(t, vars)
	if e.App.Storage == nil {
		t.Fatal("Storage is not enabled")
	}
	ctx := context.Background()
	owner := e.owner()
	name := owner.project("chat-app").str("storageBucket")
	b, err := e.App.Storage.Bucket(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
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
	return &v0Env{Env: e, bucket: b}, owner
}

func (e *v0Env) list() string { return "/v0/b/" + e.bucket.Name + "/o" }

// session returns the path of a start's X-Goog-Upload-URL, which must be absolute under base,
// the server's public URL, and the reply as A §3.10 wants it: 200, active, no body, no chunk
// granularity.
func (e *v0Env) session(r v0Reply, base string) string {
	e.t.Helper()
	e.expect(r, 200, "start")
	u := r.header.Get("X-Goog-Upload-URL")
	path, ok := strings.CutPrefix(u, base+"/v0/b/"+e.bucket.Name+"/o?name=")
	if !ok || !strings.Contains(path, "&upload_id=") || r.header.Get("X-Goog-Upload-Status") != "active" ||
		len(r.body) != 0 || r.header.Get("X-Goog-Upload-Chunk-Granularity") != "" {
		e.t.Fatalf("start: %q %v %q", u, r.header, r.body)
	}
	return u[len(base):]
}

// status checks a session reply's X-Goog-Upload-Status and X-Goog-Upload-Size-Received ("" for
// absent).
func (e *v0Env) status(r v0Reply, what string, code int, status, received string) {
	e.t.Helper()
	e.expect(r, code, what)
	if got := r.header.Get("X-Goog-Upload-Status"); got != status {
		e.t.Fatalf("%s: status %q, want %q", what, got, status)
	}
	if got := r.header.Get("X-Goog-Upload-Size-Received"); got != received {
		e.t.Fatalf("%s: size received %q, want %q", what, got, received)
	}
	if code == 200 && status == "active" && len(r.body) != 0 {
		e.t.Fatalf("%s: a body: %s", what, r.body)
	}
}

func md5Of(s string) string {
	sum := md5.Sum([]byte(s))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// recording is the part of an emulator capture the replay reads.
type recording struct {
	Steps []struct {
		Step      string `json:"step"`
		Exchanges []struct {
			Request struct {
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
				Body    struct {
					Text       *string `json:"text"`
					Base64     string  `json:"base64"`
					Length     int     `json:"length"`
					SHA256     string  `json:"sha256"`
					HeadBase64 string  `json:"headBase64"`
				} `json:"body"`
			} `json:"request"`
			Response struct {
				Status  int               `json:"status"`
				Headers map[string]string `json:"headers"`
				Body    struct {
					Text string `json:"text"`
				} `json:"body"`
			} `json:"response"`
		} `json:"exchanges"`
	} `json:"steps"`
}

// The web SDK's uploads as the emulator recorded them (firebase 13.1.0): every multipart, and the
// 700 KiB resumable in three requests, replayed on Aruma with the recording's headers and
// bodies. Each reply has the recorded status and upload status, and the object the recorded
// reply describes: name, size, md5Hash, content fields and custom metadata. What Aruma writes
// differently by decision is left aside: crc32c in base64 (ADR-023 §3-16), the stored
// contentDisposition (§3-35), and no empty metadata object (§3-40).
func TestStorageUploadWebRecorded(t *testing.T) {
	e := newUploadEnv(t, nil)
	raw, err := os.ReadFile("../oracle/storage/emulator/sdk_upload.json")
	if err != nil {
		t.Fatal(err)
	}
	var rec recording
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	const (
		recBucket = "demo-aruma-ref.appspot.com"
		recPrefix = "aruma-ref/s0c-20261023-emu/open/"
	)
	escPrefix := strings.ReplaceAll(recPrefix, "/", "%2F")
	for _, step := range rec.Steps {
		session := ""
		for i, x := range step.Exchanges {
			what := fmt.Sprintf("%s #%d", step.Step, i)
			rq := x.Request
			path := strings.ReplaceAll(strings.ReplaceAll(rq.URL, recBucket, e.bucket.Name), escPrefix, "public%2F")
			if strings.Contains(rq.URL, "upload_id=") {
				path = session
			}
			var body []byte
			switch b := rq.Body; {
			case b.Text != nil:
				body = []byte(*b.Text)
			case b.Base64 != "":
				if body, err = base64.StdEncoding.DecodeString(b.Base64); err != nil {
					t.Fatal(err)
				}
			case b.Length > 0:
				// A large body is recorded by its length and hash; the web test wrote one byte over.
				head, _ := base64.StdEncoding.DecodeString(b.HeadBase64)
				body = []byte(strings.Repeat(string(head[:1]), b.Length))
				if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != b.SHA256 {
					t.Fatalf("%s: cannot rebuild the body", what)
				}
			}
			body = []byte(strings.ReplaceAll(string(body), recPrefix, "public/"))
			header := map[string]string{}
			for k, v := range rq.Headers {
				switch k {
				case "host", "content-length", "authorization", "accept-encoding":
				default:
					header[k] = v
				}
			}
			r := e.call(rq.Method, path, firebase(e.alice), string(body), header)
			want := x.Response
			if r.status != want.Status || r.header.Get("X-Goog-Upload-Status") != want.Headers["x-goog-upload-status"] {
				t.Fatalf("%s: %d %q, recorded %d %q: %s", what, r.status, r.header.Get("X-Goog-Upload-Status"),
					want.Status, want.Headers["x-goog-upload-status"], r.body)
			}
			if want.Headers["x-goog-upload-url"] != "" {
				session = e.session(r, e.URL)
				continue
			}
			if !strings.HasPrefix(want.Body.Text, "{") {
				continue
			}
			var recorded map[string]any
			if err := json.Unmarshal([]byte(want.Body.Text), &recorded); err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{"size", "md5Hash", "contentType", "cacheControl", "contentLanguage", "contentEncoding"} {
				if fmt.Sprint(r.json[k]) != fmt.Sprint(recorded[k]) {
					t.Errorf("%s: %s %v, recorded %v", what, k, r.json[k], recorded[k])
				}
			}
			if r.str("name") != strings.ReplaceAll(recorded["name"].(string), recPrefix, "public/") || r.str("bucket") != e.bucket.Name {
				t.Errorf("%s: %s in %s", what, r.str("name"), r.str("bucket"))
			}
			if md, _ := recorded["metadata"].(map[string]any); len(md) > 0 && fmt.Sprint(r.json["metadata"]) != fmt.Sprint(md) {
				t.Errorf("%s: metadata %v, recorded %v", what, r.json["metadata"], md)
			}
			disposition := r.str("contentDisposition")
			if recorded["contentDisposition"] != "inline" {
				disposition = strings.ReplaceAll(disposition, recorded["contentDisposition"].(string), "inline; filename*=utf-8''")
			}
			if r.str("downloadTokens") == "" || !strings.HasPrefix(disposition, "inline; filename*=utf-8''") {
				t.Errorf("%s: %s", what, r.body)
			}
		}
	}
	// The resumable one is stored whole.
	o, err := e.App.Storage.Stat(context.Background(), e.bucket, "public/up/large.txt",
		storage.Principal{Kind: storage.PrincipalAdmin}, storage.SurfaceGCS, 0)
	if err != nil || o.Size != 716800 || o.ContentType != "text/plain" || o.Metadata["big"] != "yes" {
		t.Errorf("large.txt: %+v, %v", o, err)
	}
}

// Android's uploads (A §3.10-3.13): resumable always, started on /o with uploadType=resumable,
// no declared size and maybe no body; chunks by POST with the protocol and the credential; a
// finalize alone with an empty body when the file ends on a chunk's boundary; a query after a
// failed chunk; a cancel by POST.
func TestStorageUploadAndroid(t *testing.T) {
	e := newUploadEnv(t, nil)
	auth := firebase(e.alice)
	proto := func(cmd string, more ...string) map[string]string {
		h := map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": cmd,
			"x-firebase-gmpid": "1:1:android:1", "X-Firebase-Storage-Version": "Android/21.0.0"}
		for i := 0; i+1 < len(more); i += 2 {
			h[more[i]] = more[i+1]
		}
		return h
	}
	// A start without a body: putStream with no metadata.
	start := e.call("POST", e.list()+"?uploadType=resumable&name=users%2Falice%2Fa.bin", auth, "",
		proto("start", "X-Goog-Upload-Header-Content-Type", "application/x-thing"))
	s := e.session(start, e.URL)
	part := strings.Repeat("a", 256<<10)
	e.status(e.call("POST", s, auth, part, proto("upload", "X-Goog-Upload-Offset", "0")), "chunk 1", 200, "active", strconv.Itoa(256<<10))
	// The next chunk is lost on the way: the query tells the size, and the chunk is sent again.
	e.status(e.call("POST", s, auth, "", proto("query")), "query", 200, "active", strconv.Itoa(256<<10))
	e.status(e.call("POST", s, auth, part, proto("upload", "X-Goog-Upload-Offset", strconv.Itoa(256<<10))), "chunk 2", 200, "active", strconv.Itoa(512<<10))
	// Sent again after a lost reply: the overlap is ignored.
	e.status(e.call("POST", s, auth, part, proto("upload", "X-Goog-Upload-Offset", strconv.Itoa(256<<10))), "chunk 2 again", 200, "active", strconv.Itoa(512<<10))
	// The file ends on the boundary: finalize alone, empty.
	fin := e.call("POST", s, auth, "", proto("finalize", "X-Goog-Upload-Offset", strconv.Itoa(512<<10)))
	e.status(fin, "finalize", 200, "final", strconv.Itoa(512<<10))
	if fin.str("size") != strconv.Itoa(512<<10) || fin.str("contentType") != "application/x-thing" ||
		fin.str("md5Hash") != md5Of(part+part) || fin.str("downloadTokens") == "" {
		t.Fatalf("finalize: %s", fin.body)
	}
	// An empty file: a start with metadata, then finalize alone.
	s = e.session(e.call("POST", e.list()+"?name=users%2Falice%2Fempty&uploadType=resumable", auth,
		`{"contentType":"text/plain","metadata":{"k":"v"}}`, proto("start", "Content-Type", "application/json")), e.URL)
	fin = e.call("POST", s, auth, "", proto("finalize", "X-Goog-Upload-Offset", "0"))
	e.status(fin, "empty finalize", 200, "final", "0")
	if fin.str("size") != "0" || fin.str("contentType") != "text/plain" || fmt.Sprint(fin.json["metadata"]) != "map[k:v]" {
		t.Fatalf("empty: %s", fin.body)
	}
	// A cancel, by POST with the credential: 200, and twice.
	s = e.session(e.call("POST", e.list()+"?name=users%2Falice%2Fc&uploadType=resumable", auth, "", proto("start")), e.URL)
	e.status(e.call("POST", s, auth, "abc", proto("upload", "X-Goog-Upload-Offset", "0")), "chunk", 200, "active", "3")
	e.status(e.call("POST", s, auth, "", proto("cancel")), "cancel", 200, "cancelled", "3")
	e.status(e.call("POST", s, auth, "", proto("cancel")), "cancel again", 200, "cancelled", "3")
	// The next chunk is final, so Android stops instead of resending it.
	e.status(e.call("POST", s, auth, "d", proto("upload, finalize", "X-Goog-Upload-Offset", "3")), "chunk after cancel", 400, "final", "3")
	e.status(e.call("POST", s, auth, "", proto("query")), "query after cancel", 200, "cancelled", "3")
	e.expect(e.call("GET", e.obj("users/alice/c"), auth, "", nil), 404, "the cancelled object")
}

// iOS's uploads (A §1.1-1.3, §3.10-3.13): the start on /o/{object} with the name in the query,
// the path and the body (bucket "" and metadata null with it), the size and type in
// X-Goog-Upload-Content-*; then the whole file in one PUT without any credential; a query after
// final answers the object, which iOS takes as the result; a cancel by PUT.
func TestStorageUploadIOS(t *testing.T) {
	e := newUploadEnv(t, nil)
	data := strings.Repeat("ios-", 1000)
	ios := map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start",
		"X-Goog-Upload-Content-Length": strconv.Itoa(len(data)), "X-Goog-Upload-Content-Type": "image/png",
		"Content-Type": "application/json; charset=UTF-8", "x-firebase-gmpid": "1:1:ios:1"}
	start := e.call("POST", e.obj("users/alice/p.png")+"?uploadType=resumable&name=users%2Falice%2Fp.png", firebase(e.alice),
		`{"name":"users/alice/p.png","bucket":"","contentType":"image/png","metadata":null}`, ios)
	s := e.session(start, e.URL)
	chunk := func(cmd string, offset int, body string) map[string]string {
		return map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": cmd,
			"X-Goog-Upload-Offset": strconv.Itoa(offset), "X-Goog-Upload-Content-Length": strconv.Itoa(len(data)),
			"User-Agent": "app (GTMSUF/1)"}
	}
	e.status(e.call("PUT", s, "", "", map[string]string{"X-Goog-Upload-Command": "query"}), "query", 200, "active", "0")
	fin := e.call("PUT", s, "", data, chunk("upload, finalize", 0, data))
	e.status(fin, "the file in one request", 200, "final", strconv.Itoa(len(data)))
	if fin.str("name") != "users/alice/p.png" || fin.str("contentType") != "image/png" || fin.str("md5Hash") != md5Of(data) {
		t.Fatalf("finalize: %s", fin.body)
	}
	// After a lost reply, the query is the result: final and the same object.
	q := e.call("PUT", s, "", "", map[string]string{"X-Goog-Upload-Command": "query"})
	e.status(q, "query after final", 200, "final", strconv.Itoa(len(data)))
	if q.str("generation") != fin.str("generation") || q.str("downloadTokens") != fin.str("downloadTokens") {
		t.Fatalf("query after final: %s, finalize %s", q.body, fin.body)
	}
	// The finalize sent again answers the same.
	again := e.call("PUT", s, "", "", chunk("finalize", len(data), ""))
	e.status(again, "finalize again", 200, "final", strconv.Itoa(len(data)))
	if again.str("generation") != fin.str("generation") {
		t.Fatalf("finalize again: %s", again.body)
	}
	// A cancel by PUT, without a credential; then a cancel of the finished session is final.
	s2 := e.session(e.call("POST", e.obj("users/alice/q.png")+"?uploadType=resumable&name=users%2Falice%2Fq.png",
		firebase(e.alice), `{"name":"users/alice/q.png","bucket":""}`, ios), e.URL)
	e.status(e.call("PUT", s2, "", "", map[string]string{"X-Goog-Upload-Command": "cancel"}), "cancel", 200, "cancelled", "0")
	e.status(e.call("PUT", s, "", "", map[string]string{"X-Goog-Upload-Command": "cancel"}), "cancel after final", 400, "final", strconv.Itoa(len(data)))
}

// The session URL is the credential (ADR-022 §7): Authorization on a chunk is ignored, a broken
// one included, and another user's token does not make the upload theirs; the identity bound at
// start decides at finalize. An unknown id is 404 without a status.
func TestStorageUploadSessionCredential(t *testing.T) {
	e := newUploadEnv(t, nil)
	h := func(cmd string, offset int) map[string]string {
		return map[string]string{"X-Goog-Upload-Command": cmd, "X-Goog-Upload-Offset": strconv.Itoa(offset)}
	}
	start := map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start"}
	s := e.session(e.call("POST", e.list()+"?name=users%2Falice%2Fx", firebase(e.alice), "", start), e.URL)
	e.status(e.call("POST", s, "Firebase not-a-token", "abc", h("upload", 0)), "a broken credential", 200, "active", "3")
	e.status(e.call("POST", s, "Basic YTpi", "def", h("upload", 3)), "another scheme", 200, "active", "6")
	fin := e.call("POST", s, firebase(e.bob), "", h("finalize", 6))
	e.status(fin, "finalized with bob's token", 200, "final", "6")
	if fin.str("name") != "users/alice/x" {
		t.Fatalf("finalize: %s", fin.body)
	}
	// Bob's own session on alice's folder is refused at finalize, whoever's token is on it.
	s = e.session(e.call("POST", e.list()+"?name=users%2Falice%2Fy", firebase(e.bob), "", start), e.URL)
	e.status(e.call("POST", s, firebase(e.alice), "abc", h("upload, finalize", 0)), "bob's upload", 403, "final", "3")
	// An unknown session, and one of the right id on another name.
	unknown := e.list() + "?name=users%2Falice%2Fx&upload_id=AAAAAAAAAAAAAAAAAAAAAA&upload_protocol=resumable"
	e.status(e.call("POST", unknown, "", "", map[string]string{"X-Goog-Upload-Command": "query"}), "an unknown session", 404, "", "")
	other := strings.Replace(s, "name=users%2Falice%2Fy", "name=users%2Falice%2Fz", 1)
	e.status(e.call("PUT", other, "", "", map[string]string{"X-Goog-Upload-Command": "query"}), "another name", 404, "", "")
	// A start with a credential that does not verify is 401, as any request.
	e.expect(e.call("POST", e.list()+"?name=public%2Fz", "Firebase not-a-token", "", start), 401, "start with a broken token")
}

// The session URL is absolute from public_url, never from the Host header: behind a proxy the
// client reaches the session where public_url says (ADR-022 §6.1, D §7.2).
func TestStorageUploadSessionURLBehindProxy(t *testing.T) {
	e, _ := newUploadProject(t, map[string]string{"ARUMA_PUBLIC_URL": "https://files.example.test"})
	r := e.call("POST", e.list()+"?name=public%2Fa", "", "", map[string]string{"X-Goog-Upload-Protocol": "resumable",
		"X-Goog-Upload-Command": "start", "Host": "10.0.0.7:8080", "X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "http"})
	s := e.session(r, "https://files.example.test")
	// The proxy forwards the path as is.
	e.status(e.call("POST", s, "", "abc", map[string]string{"X-Goog-Upload-Command": "upload, finalize", "X-Goog-Upload-Offset": "0"}),
		"through the proxy", 200, "final", "3")
}

// The refusals of a session (ADR-022 §6.1, ADR-023 §3-20, §3-23): every terminal one is final
// and answers its repeats and queries the same; a gap is 400 active with the size received, so
// the client queries; md5Hash that does not match is 400 and no object, resumable or multipart.
func TestStorageUploadRefusals(t *testing.T) {
	e := newUploadEnv(t, nil)
	auth := firebase(e.alice)
	h := func(cmd string, offset int) map[string]string {
		return map[string]string{"X-Goog-Upload-Command": cmd, "X-Goog-Upload-Offset": strconv.Itoa(offset)}
	}
	start := func(name, body string, declared int) string {
		hd := map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start"}
		if declared >= 0 {
			hd["X-Goog-Upload-Header-Content-Length"] = strconv.Itoa(declared)
		}
		return e.session(e.call("POST", e.list()+"?name="+escapeName(name), auth, body, hd), e.URL)
	}
	query := func(s string) v0Reply {
		return e.call("POST", s, "", "", map[string]string{"X-Goog-Upload-Command": "query"})
	}

	// A gap, then an overlap, then the rest (raw_resumable.json's steps, with production's rule).
	s := start("public/res.txt", `{"name":"public/res.txt"}`, 20)
	e.status(e.call("POST", s, auth, "0123456789", h("upload", 0)), "upload 0..10", 200, "active", "10")
	e.status(e.call("POST", s, auth, "xxxxx", h("upload", 15)), "a gap", 400, "active", "10")
	e.status(e.call("POST", s, auth, "56789abcde", h("upload", 5)), "an overlap", 200, "active", "15")
	fin := e.call("POST", s, auth, "fghij", h("upload, finalize", 15))
	e.status(fin, "the rest", 200, "final", "20")
	if fin.str("md5Hash") != md5Of("0123456789abcdefghij") {
		t.Fatalf("res.txt: %s", fin.body)
	}
	// A repeat of the last chunk, or an empty finalize, is answered the saved result; a byte past
	// the object is refused, never dropped while the client reports it stored (ADR-023 §3-86).
	again := e.call("POST", s, auth, "fghij", h("upload, finalize", 15))
	e.status(again, "the last chunk again", 200, "final", "20")
	if again.str("generation") != fin.str("generation") {
		t.Fatalf("the last chunk again: %s", again.body)
	}
	e.status(e.call("POST", s, auth, "", h("finalize", 20)), "an empty finalize again", 200, "final", "20")
	e.status(e.call("POST", s, auth, "z", h("upload", 20)), "a byte after final", 400, "final", "20")
	e.status(e.call("POST", s, auth, "ijklm", h("upload, finalize", 18)), "new bytes over the end", 400, "final", "20")
	if r := e.call("GET", e.obj("public/res.txt"), auth, "", nil); r.str("size") != "20" || r.str("generation") != fin.str("generation") {
		t.Fatalf("res.txt after the refused bytes: %s", r.body)
	}

	// A finalize short of the declared size: final, and so is every repeat and query.
	s = start("public/short.txt", "", 10)
	e.status(e.call("POST", s, auth, "abcd", h("upload, finalize", 0)), "4 of 10 bytes", 400, "final", "4")
	e.status(e.call("POST", s, auth, "", h("finalize", 4)), "finalize again", 400, "final", "4")
	e.status(query(s), "query", 400, "final", "4")
	e.expect(e.call("GET", e.obj("public/short.txt"), auth, "", nil), 404, "the short object")

	// md5Hash at start that is not the bytes'.
	s = start("public/md5.txt", `{"md5Hash":"`+md5Of("other")+`"}`, -1)
	e.status(e.call("POST", s, auth, "data", h("upload, finalize", 0)), "md5Hash differs", 400, "final", "4")
	e.expect(e.call("GET", e.obj("public/md5.txt"), auth, "", nil), 404, "the md5 object")
	s = start("public/md5ok.txt", `{"md5Hash":"`+md5Of("data")+`"}`, -1)
	e.status(e.call("POST", s, auth, "data", h("upload, finalize", 0)), "md5Hash matches", 200, "final", "4")

	// The rules at finalize: anonymous on alice's folder.
	s = e.session(e.call("POST", e.list()+"?name=users%2Falice%2Fanon", "", "",
		map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start"}), e.URL)
	e.status(e.call("POST", s, "", "abc", h("upload", 0)), "anonymous bytes", 200, "active", "3")
	e.status(e.call("POST", s, "", "", h("finalize", 3)), "the rules", 403, "final", "3")
	e.status(e.call("POST", s, "", "", h("finalize", 3)), "the rules again", 403, "final", "3")
	e.status(query(s), "query after the rules", 403, "final", "3")

	// Multipart: md5Hash that differs is 400 and no object; a malformed body is 400.
	mp := func(js, data string) string {
		return "--b\r\nContent-Type: application/json; charset=utf-8\r\n\r\n" + js + "\r\n--b\r\nContent-Type: text/plain\r\n\r\n" + data + "\r\n--b--"
	}
	mph := map[string]string{"X-Goog-Upload-Protocol": "multipart", "Content-Type": "multipart/related; boundary=b"}
	e.expect(e.call("POST", e.list()+"?name=public%2Fmp.txt", auth, mp(`{"md5Hash":"`+md5Of("x")+`"}`, "data"), mph), 400, "multipart md5Hash")
	e.expect(e.call("GET", e.obj("public/mp.txt"), auth, "", nil), 404, "the multipart object")
	e.expect(e.call("POST", e.list()+"?name=public%2Fmp.txt", auth, mp(`{}`, "data\r\n--b\r\nContent-Type: text/plain\r\n\r\nthird"), mph), 400, "three parts")
	// The JSON's name, when the query has none (ADR-023 §3-26); the query's wins otherwise.
	r := e.call("POST", e.list(), auth, mp(`{"name":"public/in-json.txt","contentType":"text/plain"}`, "data"), mph)
	e.expect(r, 200, "a name in the JSON alone")
	if r.str("name") != "public/in-json.txt" || r.str("contentType") != "text/plain" {
		t.Fatalf("name in the JSON: %s", r.body)
	}
	r = e.call("POST", e.list()+"?name=public%2Fq.txt", auth, mp(`{"name":"public/meta.txt"}`, "data"), mph)
	if r.status != 200 || r.str("name") != "public/q.txt" || r.str("contentType") != "application/octet-stream" {
		t.Fatalf("query name: %s", r.body)
	}
	// A media upload, by its Content-Type; a planted token never holds.
	r = e.call("POST", e.obj("public/raw.txt"), auth, "raw body", map[string]string{"Content-Type": "text/plain"})
	if r.status != 200 || r.str("contentType") != "text/plain" || r.str("size") != "8" {
		t.Fatalf("media: %s", r.body)
	}
	r = e.call("POST", e.list()+"?name=public%2Ftok.txt", auth, mp(`{"metadata":{"firebaseStorageDownloadTokens":"mine","k":"v"}}`, "d"), mph)
	if r.status != 200 || r.str("downloadTokens") == "mine" || fmt.Sprint(r.json["metadata"]) != "map[k:v]" {
		t.Fatalf("planted token: %s", r.body)
	}
}

// A chunk whose connection breaks halfway keeps what arrived (ADR-022 §6.3, ADR-023 §3-53): the
// reply, when the client still reads one, is 400 active with the bytes kept, a query reports
// them, and the rest sent again from there completes the object whole. The first chunk of a
// session is the case net/http's cancelled context used to lose.
func TestStorageUploadCutBody(t *testing.T) {
	e := newUploadEnv(t, nil)
	auth := firebase(e.alice)
	data := strings.Repeat("0123456789", 10)
	s := e.session(e.call("POST", e.list()+"?name=users%2Falice%2Fcut", auth, "",
		map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start"}), e.URL)
	conn, err := net.Dial("tcp", strings.TrimPrefix(e.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: x\r\nX-Goog-Upload-Command: upload, finalize\r\nX-Goog-Upload-Offset: 0\r\n"+
		"Content-Length: %d\r\n\r\n%s", s, len(data), data[:50])
	// The client stops sending: its half of the connection closes.
	if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 || resp.Header.Get("X-Goog-Upload-Status") != "active" || resp.Header.Get("X-Goog-Upload-Size-Received") != "50" {
		t.Fatalf("the cut chunk: %d %v", resp.StatusCode, resp.Header)
	}
	e.status(e.call("POST", s, "", "", map[string]string{"X-Goog-Upload-Command": "query"}), "query after the cut", 200, "active", "50")
	fin := e.call("POST", s, auth, data[50:], map[string]string{"X-Goog-Upload-Command": "upload, finalize", "X-Goog-Upload-Offset": "50"})
	e.status(fin, "the rest", 200, "final", "100")
	if fin.str("size") != "100" || fin.str("md5Hash") != md5Of(data) {
		t.Fatalf("the object: %s", fin.body)
	}
}

// The quota is a terminal refusal too: 402 final, on the chunk that goes over (ADR-022 §6.5),
// and at start when the declared size does not fit.
func TestStorageUploadQuota(t *testing.T) {
	e := newUploadEnv(t, map[string]string{"ARUMA_STORAGE_PROJECT_BYTES": strconv.Itoa(1 << 20),
		"ARUMA_STORAGE_OBJECT_BYTES": strconv.Itoa(1 << 20)})
	auth := firebase(e.alice)
	big := strings.Repeat("q", 700<<10)
	if r := e.call("POST", e.obj("public/first"), auth, big, nil); r.status != 200 {
		t.Fatalf("first: %d %s", r.status, r.body)
	}
	hd := map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start"}
	s := e.session(e.call("POST", e.list()+"?name=public%2Fsecond", auth, "", hd), e.URL)
	e.status(e.call("POST", s, auth, big, map[string]string{"X-Goog-Upload-Command": "upload", "X-Goog-Upload-Offset": "0"}),
		"over the quota", 402, "final", "0")
	e.status(e.call("POST", s, "", "", map[string]string{"X-Goog-Upload-Command": "query"}), "query", 402, "final", "0")
	hd["X-Goog-Upload-Header-Content-Length"] = strconv.Itoa(700 << 10)
	e.expect(e.call("POST", e.list()+"?name=public%2Fthird", auth, "", hd), 402, "a declared size over the quota")
}

// An upload longer than the server's 60-second timeouts gets its reply, and a slowloris is cut
// with 408 once a 30-second window brings less than min_bytes_per_second (ADR-022 §6.4). These
// run on the server's own listener (server.Run), whose timeouts are the production ones, on
// S3-B's port 9211; about 70 seconds.
func TestStorageUploadSlow(t *testing.T) {
	if testing.Short() {
		t.Skip("70 seconds of slow uploads")
	}
	const addr = "127.0.0.1:9211"
	e, owner := newUploadProject(t, map[string]string{"ARUMA_LISTEN": addr, "ARUMA_METRICS_LISTEN": "-", "ARUMA_PUBLIC_URL": "http://" + addr})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.App.Server.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	for {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			c.Close()
			break
		}
		select {
		case err := <-done:
			t.Fatalf("server: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	base := "http://" + addr
	// The users sign in once the public URL answers: the service account's token comes from it.
	users := e.rtdbUsers(owner)
	users.create("chat-app", "alice")
	e.alice = users.signIn("chat-app", "alice").str("idToken")
	auth := firebase(e.alice)
	hd := map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start"}
	slow := e.Env
	real := *slow
	real.URL = base
	ve := &v0Env{Env: &real, bucket: e.bucket, alice: e.alice}

	t.Run("long", func(t *testing.T) {
		t.Parallel()
		s := ve.session(ve.call("POST", e.list()+"?name=users%2Falice%2Flong", auth, "", hd), base)
		// 16 KiB every two seconds for 64 seconds: 8 KiB/s, twice the minimum rate.
		const parts, size = 32, 16 << 10
		pr, pw := io.Pipe()
		go func() {
			for range parts {
				if _, err := pw.Write([]byte(strings.Repeat("s", size))); err != nil {
					return
				}
				time.Sleep(2 * time.Second)
			}
			pw.Close()
		}()
		req, _ := http.NewRequest("POST", base+s, pr)
		req.ContentLength = parts * size
		req.Header.Set("X-Goog-Upload-Command", "upload, finalize")
		req.Header.Set("X-Goog-Upload-Offset", "0")
		began := time.Now()
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("after %v: %v", time.Since(began), err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		wiretest.AssertNoNull(t, body)
		if resp.StatusCode != 200 || resp.Header.Get("X-Goog-Upload-Status") != "final" || time.Since(began) < 60*time.Second ||
			!strings.Contains(string(body), `"size":"`+strconv.Itoa(parts*size)+`"`) {
			t.Fatalf("after %v: %d %v %s", time.Since(began), resp.StatusCode, resp.Header, body)
		}
	})
	t.Run("slowloris", func(t *testing.T) {
		t.Parallel()
		s := ve.session(ve.call("POST", e.list()+"?name=users%2Falice%2Floris", auth, "", hd), base)
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: %s\r\nX-Goog-Upload-Command: upload\r\nX-Goog-Upload-Offset: 0\r\n"+
			"Content-Length: 1000000\r\n\r\n0123456789", s, addr)
		began := time.Now()
		// One byte every five seconds: far below 4 KiB/s.
		go func() {
			for range 20 {
				time.Sleep(5 * time.Second)
				if _, err := conn.Write([]byte("x")); err != nil {
					return
				}
			}
		}()
		_ = conn.SetReadDeadline(time.Now().Add(50 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("after %v: %v", time.Since(began), err)
		}
		resp.Body.Close()
		if took := time.Since(began); resp.StatusCode != http.StatusRequestTimeout || took < 29*time.Second || took > 40*time.Second {
			t.Fatalf("after %v: %d %v", took, resp.StatusCode, resp.Header)
		}
		// What arrived is kept: the client resumes from it.
		q := ve.call("POST", s, "", "", map[string]string{"X-Goog-Upload-Command": "query"})
		n, err := strconv.Atoi(q.header.Get("X-Goog-Upload-Size-Received"))
		if q.status != 200 || q.header.Get("X-Goog-Upload-Status") != "active" || err != nil || n < 10 {
			t.Fatalf("query after the cut: %d %v", q.status, q.header)
		}
	})
}
