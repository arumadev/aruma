package console

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/storage/core"
)

// issue asks for an upload session and returns its URL's upload id.
func (e *env) issue(body any) (response, string) {
	e.t.Helper()
	got := e.do(amalSession, "POST", path("/uploads"), body)
	if got.code != http.StatusOK {
		return got, ""
	}
	u, err := url.Parse(got.json["uploadUrl"].(string))
	if err != nil {
		e.t.Fatal(err)
	}
	return got, u.Query().Get("upload_id")
}

// The dashboard's upload: a small JSON issues a session bound to the account, as an admin, with
// its declared size and a life of an hour; its URL is /v0's, absolute from public_url; the chunks
// go there without a cookie (the session's id alone, as /v0 hands them to core); the issue and
// the finalize are both in the audit log, and neither holds the id.
func TestIssueUploadSession(t *testing.T) {
	e := newEnv(t, true)
	name := "photos/2026/naïve 'q' 1+1%;x.png"
	got, id := e.issue(map[string]any{"name": name, "size": 5, "contentType": "image/png",
		"cacheControl": "private, max-age=60", "metadata": map[string]string{"album": "summer"}})
	if got.code != http.StatusOK || id == "" {
		t.Fatalf("issue: %d %s", got.code, got.body)
	}
	if got.header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q: the reply carries the session's credential", got.header.Get("Cache-Control"))
	}
	wantURL := publicURL + "/v0/b/chat-app.aruma.test/o?name=photos%2F2026%2Fna%C3%AFve%20'q'%201%2B1%25%3Bx.png&upload_id=" +
		id + "&upload_protocol=resumable"
	if got.json["uploadUrl"] != wantURL {
		t.Errorf("uploadUrl\n got %v\nwant %s", got.json["uploadUrl"], wantURL)
	}
	if got.json["size"] != float64(5) || got.json["name"] != name || got.json["bucket"] != "chat-app.aruma.test" {
		t.Errorf("reply: %s", got.body)
	}
	exp, err := time.Parse(time.RFC3339, got.json["expireTime"].(string))
	if err != nil || time.Until(exp) < 59*time.Minute || time.Until(exp) > 61*time.Minute {
		t.Errorf("expireTime %v: want an hour from now", got.json["expireTime"])
	}

	// The session as stored: the dashboard's, an admin's bound to amal, the size declared.
	var surface, kind, principal string
	var declared int64
	err = e.pool.QueryRow(t.Context(), `SELECT surface, principal_kind, principal::text, declared_size
		FROM storage.upload_sessions WHERE project_number = $1 AND name = $2`, chatApp, name).
		Scan(&surface, &kind, &principal, &declared)
	if err != nil {
		t.Fatal(err)
	}
	if surface != "console" || kind != "admin" || !strings.Contains(principal, amal) || declared != 5 {
		t.Errorf("session: surface %s, kind %s, principal %s, declared %d", surface, kind, principal, declared)
	}
	issued, ok := e.lastAudit("storage.upload.issue")
	if !ok || issued.actor != amal || issued.target != name || issued.details["size"] != float64(5) {
		t.Errorf("issue audit: %+v (found %v)", issued, ok)
	}

	// The chunks, as /v0 passes them on: by the id, with no principal.
	st, err := e.svc.WriteUpload(t.Context(), e.bucket, name, id, nil,
		core.Chunk{Offset: 0, Body: strings.NewReader("hel"), Length: 3, Total: -1})
	if err != nil || st.Status != core.UploadActive {
		t.Fatalf("first chunk: %+v %v", st, err)
	}
	// After its first byte the session lives the dashboard's hour, not /v0's week.
	var life time.Duration
	err = e.pool.QueryRow(t.Context(), `SELECT extract(epoch FROM expires_at - created_at)::bigint * interval '1 second'
		FROM storage.upload_sessions WHERE project_number = $1 AND name = $2`, chatApp, name).Scan(&life)
	if err != nil {
		t.Fatal(err)
	}
	if life != time.Hour {
		t.Errorf("a dashboard session lives %v, want an hour", life)
	}
	st, err = e.svc.WriteUpload(t.Context(), e.bucket, name, id, nil,
		core.Chunk{Offset: 3, Body: strings.NewReader("lo"), Length: 2, Finalize: true, Total: 5})
	if err != nil || st.Status != core.UploadFinal || st.Object == nil {
		t.Fatalf("finalize: %+v %v", st, err)
	}
	obj := e.do(amalSession, "GET", path("/object", "name", name), nil)
	if obj.code != http.StatusOK || obj.json["contentType"] != "image/png" || obj.json["size"] != float64(5) ||
		obj.json["cacheControl"] != "private, max-age=60" || obj.json["metadata"].(map[string]any)["album"] != "summer" {
		t.Errorf("the object uploaded: %s", obj.body)
	}
	done, ok := e.lastAudit("storage.object.upload")
	if !ok || done.actor != amal || done.target != name {
		t.Errorf("finalize audit: %+v (found %v)", done, ok)
	}
	for _, a := range e.audits() {
		for _, v := range a.details {
			if s, _ := v.(string); strings.Contains(s, id) {
				t.Errorf("the audit log holds the upload id: %+v", a)
			}
		}
	}
}

// The size is declared and bound: a session without one is not issued, and a finalize at
// another size is refused, final.
func TestUploadSizeIsMandatoryAndBound(t *testing.T) {
	e := newEnv(t, true)
	for _, c := range []struct {
		body any
		want string
	}{
		{map[string]any{"name": "a.bin"}, "size"},
		{map[string]any{"name": "a.bin", "size": nil}, "size"},
		{map[string]any{"name": "a.bin", "size": -1}, "size"},
		{map[string]any{"name": "a.bin", "size": "5"}, "Invalid JSON"},
		{map[string]any{"size": 5}, "name"},
		{map[string]any{"name": "a.bin", "size": 5, "protocol": "x"}, "Invalid JSON"},
		{map[string]any{"name": "a.bin", "size": 6 << 40}, "larger than this server accepts"},
		{map[string]any{"name": "a.bin", "size": 1, "metadata": map[string]string{core.TokensKey: "planted"}}, core.TokensKey},
		{map[string]any{"name": "a.bin", "size": 1, "contentType": "text/plain\r\nX: y"}, ""},
	} {
		got, _ := e.issue(c.body)
		if got.code != http.StatusBadRequest || !strings.Contains(string(got.body), c.want) {
			t.Errorf("issue %v: %d %s, want 400 about %q", c.body, got.code, got.body, c.want)
		}
	}
	if n := len(e.audits()); n != 0 {
		t.Errorf("%d audit lines for sessions never issued", n)
	}

	_, id := e.issue(map[string]any{"name": "a.bin", "size": 5})
	st, err := e.svc.WriteUpload(t.Context(), e.bucket, "a.bin", id, nil,
		core.Chunk{Offset: 0, Body: strings.NewReader("four"), Length: 4, Finalize: true, Total: 4})
	if err == nil && st.Err == nil {
		t.Fatalf("a finalize at 4 bytes of 5 declared: %+v", st)
	}
	if st, _ := e.svc.QueryUpload(t.Context(), e.bucket, "a.bin", id); st.Status != core.UploadFinal {
		t.Errorf("after a finalize at another size the session is %v, want final", st.Status)
	}
}

// A session the audit log cannot record is not handed out: it is cancelled, and its id never
// leaves the server.
func TestUploadNotIssuedWithoutAudit(t *testing.T) {
	e := newEnv(t, true)
	e.failAudit = true
	got, _ := e.issue(map[string]any{"name": "a.bin", "size": 5})
	if got.code != http.StatusInternalServerError || strings.Contains(string(got.body), "upload_id") {
		t.Fatalf("issue without the audit log: %d %s", got.code, got.body)
	}
	var state string
	if err := e.pool.QueryRow(t.Context(), `SELECT state FROM storage.upload_sessions WHERE name = 'a.bin'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" {
		t.Errorf("the session is %s, want cancelled", state)
	}
}

// The upload starts per address apply to the dashboard's sessions as to /v0's.
func TestUploadStartRate(t *testing.T) {
	e := newEnv(t, true, func(l *core.Limits) { l.UploadStartsPerSecond = 2 })
	n := e.svc.Limits().UploadStartsPerSecond
	limited := false
	for range n + 1 {
		got, _ := e.issue(map[string]any{"name": "a.bin", "size": 1})
		if got.code == http.StatusTooManyRequests {
			limited = got.header.Get("Retry-After") != ""
			break
		}
	}
	if !limited {
		t.Errorf("%d starts in a burst were all allowed", n+1)
	}
}
