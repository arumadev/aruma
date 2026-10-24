package acceptance

import (
	"encoding/json"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	storage "github.com/arumadev/aruma/internal/services/storage/core"
)

// The dashboard's upload end to end (ADR-022 §15): the console issues a session behind its
// cookie and CSRF token, and the browser sends the bytes to the session's /v0 URL with neither
// the cookie nor a credential, the URL being the session's only one. The object lands as the
// owner's upload, with the metadata the issue named and a download token, and the audit log
// holds both the issue and the upload, by the owner's account. The declared size is bound: a
// session finalized short of it stores nothing.
func TestStorageConsoleUploadThroughV0(t *testing.T) {
	e, owner := newUploadProject(t, nil)
	var account uuid.UUID
	if err := e.Pool.QueryRow(t.Context(), `SELECT id FROM control.accounts WHERE email = 'owner@example.com'`).Scan(&account); err != nil {
		t.Fatal(err)
	}
	const name = "reports/q3 & q4.txt"
	data := strings.Repeat("aruma! ", 40000) // 280,000 bytes: more than one chunk
	issue := owner.mustOK(owner.do("POST", "/console/v1/projects/chat-app/storage/uploads", map[string]any{
		"name": name, "size": len(data), "contentType": "text/plain", "metadata": map[string]string{"from": "console"},
	}))
	if !strings.Contains(issue.Header.Get("Cache-Control"), "no-store") {
		t.Errorf("the issue's reply, which carries the session id, is cacheable: %q", issue.Header.Get("Cache-Control"))
	}
	if issue.str("bucket") != e.bucket.Name || issue.str("name") != name || issue.str("startBy") == "" || issue.str("expireTime") == "" {
		t.Fatalf("issue: %s", issue.Body)
	}
	// The URL is the shape a client's own start answers with, absolute from public_url.
	u := issue.str("uploadUrl")
	if rest, ok := strings.CutPrefix(u, e.URL+"/v0/b/"+e.bucket.Name+"/o?name="); !ok || !strings.Contains(rest, "&upload_id=") {
		t.Fatalf("uploadUrl %q", u)
	}
	s := u[len(e.URL):]
	_, session, _ := strings.Cut(s, "&upload_id=")
	session, _, _ = strings.Cut(session, "&")

	// No cookie and no Authorization from here on: e.call sends neither.
	cmd := func(c string, offset int) map[string]string {
		return map[string]string{"X-Goog-Upload-Command": c, "X-Goog-Upload-Offset": strconv.Itoa(offset)}
	}
	half := 256 << 10
	e.status(e.call("POST", s, "", "", map[string]string{"X-Goog-Upload-Command": "query"}), "query", 200, "active", "0")
	e.status(e.call("POST", s, "", data[:half], cmd("upload", 0)), "chunk 1", 200, "active", strconv.Itoa(half))
	fin := e.call("POST", s, "", data[half:], cmd("upload, finalize", half))
	e.status(fin, "finalize", 200, "final", strconv.Itoa(len(data)))
	if fin.str("name") != name || fin.str("size") != strconv.Itoa(len(data)) || fin.str("md5Hash") != md5Of(data) ||
		fin.str("contentType") != "text/plain" || fin.str("downloadTokens") == "" {
		t.Fatalf("finalize: %s", fin.body)
	}

	// The object, as an admin reads it: the bytes and the metadata the issue named.
	admin := storage.Principal{Kind: storage.PrincipalAdmin, ServiceAccount: "check@chat-app"}
	o, body, err := e.App.Storage.Open(t.Context(), e.bucket, name, admin, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	body.Close()
	if err != nil || string(got) != data {
		t.Fatalf("the stored bytes: %d of %d, %v", len(got), len(data), err)
	}
	if o.ContentType != "text/plain" || o.Metadata["from"] != "console" || len(o.DownloadTokens) != 1 {
		t.Errorf("the stored object: %+v", o)
	}
	// And to the dashboard.
	shown := owner.mustOK(owner.do("GET", "/console/v1/projects/chat-app/storage/object?name="+url.QueryEscape(name), nil))
	if shown.str("name") != name || shown.str("contentType") != "text/plain" {
		t.Errorf("the dashboard's object: %s", shown.Body)
	}

	// The audit log: the issue, then the upload core records when it commits, both by the owner.
	entries := owner.mustOK(owner.do("GET", "/console/v1/projects/chat-app/auditLog?pageSize=10", nil)).list("entries")
	var actions []string
	for _, en := range entries {
		if en["target"] != name {
			continue
		}
		actions = append(actions, en["action"].(string))
		if en["actorType"] != "account" || en["actorId"] != account.String() {
			t.Errorf("%v by %v %v, want the owner's account %s", en["action"], en["actorType"], en["actorId"], account)
		}
		if details, _ := en["details"].(map[string]any); details["size"] != float64(len(data)) {
			t.Errorf("%v: details %v, want size %d", en["action"], en["details"], len(data))
		}
		if raw, _ := json.Marshal(en); strings.Contains(string(raw), session) {
			t.Errorf("%v: the log carries the session id: %s", en["action"], raw)
		}
	}
	if want := []string{"storage.object.upload", "storage.upload.issue"}; !slices.Equal(actions, want) {
		t.Errorf("audit for %q, newest first: %v, want %v", name, actions, want)
	}

	// The declared size is bound: a session that finalizes short of it is refused, final, and
	// stores nothing.
	short := owner.mustOK(owner.do("POST", "/console/v1/projects/chat-app/storage/uploads", map[string]any{
		"name": "reports/short.txt", "size": 10,
	}))
	s = short.str("uploadUrl")[len(e.URL):]
	r := e.call("POST", s, "", "abc", cmd("upload, finalize", 0))
	e.expect(r, 400, "short finalize")
	if r.header.Get("X-Goog-Upload-Status") != "final" {
		t.Errorf("short finalize: status %q, want final", r.header.Get("X-Goog-Upload-Status"))
	}
	if _, err := e.App.Storage.Stat(t.Context(), e.bucket, "reports/short.txt", admin, storage.SurfaceGCS, 0); err == nil {
		t.Error("a session finalized short of its declared size stored an object")
	}
}
