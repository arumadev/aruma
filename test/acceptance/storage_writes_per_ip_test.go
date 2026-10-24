package acceptance

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// upload_requests_per_ip is one budget per address across /v0 and GCS (ADR-022 §17): app.go
// gives both surfaces the same count, so with a limit of 1 a write held open on one refuses a
// write from the same address on the other, each in its own shape (/v0 503 with Retry-After,
// GCS 429 rateLimitExceeded), until the held write ends. Both probes carry no credential: the
// count is taken before authentication, as the session routes need none.
func TestStorageWritesPerIPAcrossSurfaces(t *testing.T) {
	e, owner := newUploadProject(t, map[string]string{"ARUMA_STORAGE_UPLOAD_REQUESTS_PER_IP": "1"})
	_, key := owner.serviceAccountKey("chat-app", "storage-admin")
	sa, _, err := exchange(t, key)
	if err != nil {
		t.Fatal(err)
	}
	const body = "0123456789"

	// gcsProbe is a GCS write without a credential: 429 while the address's place is taken, its
	// 401 otherwise.
	gcsProbe := func() (int, string) {
		req, _ := http.NewRequest("DELETE", e.URL+"/storage/v1/b/"+e.bucket.Name+"/o/probe", nil)
		return send(t, req)
	}
	// v0Probe is a /v0 write without a credential: 503 while the place is taken, its 403
	// otherwise.
	v0Probe := func() (int, string) {
		req, _ := http.NewRequest("POST", e.URL+e.obj("probe")+"?create_token=true", nil)
		return send(t, req)
	}

	// A /v0 chunk held open: the session URL needs no credential, and public/ is open.
	s := e.session(e.call("POST", e.list()+"?name=public%2Fheld", "", "",
		map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start"}), e.URL)
	finish := hold(t, e.URL, fmt.Sprintf("POST %s HTTP/1.1\r\nHost: x\r\nX-Goog-Upload-Command: upload, finalize\r\n"+
		"X-Goog-Upload-Offset: 0\r\nContent-Length: %d\r\n\r\n%s", s, len(body), body[:5]), body[5:])
	refusedWhile(t, "a GCS write while a /v0 write is held", gcsProbe, 429, `"rateLimitExceeded"`, "Retry-After=1 ")
	if resp := finish(); resp.StatusCode != 200 || resp.Header.Get("X-Goog-Upload-Status") != "final" {
		t.Fatalf("the held /v0 chunk: %d %v", resp.StatusCode, resp.Header)
	}
	if code, got := gcsProbe(); code != 401 {
		t.Fatalf("a GCS write once the /v0 write is done: %d %s", code, got)
	}

	// A GCS session chunk held open: its upload_id is its credential.
	req, _ := http.NewRequest("POST", e.URL+"/upload/storage/v1/b/"+e.bucket.Name+"/o?uploadType=resumable&name=held2",
		strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+sa)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, ok := strings.CutPrefix(resp.Header.Get("Location"), e.URL)
	if resp.StatusCode != 200 || !ok {
		t.Fatalf("GCS start: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	finish = hold(t, e.URL, fmt.Sprintf("PUT %s HTTP/1.1\r\nHost: x\r\nContent-Range: bytes 0-%d/%d\r\n"+
		"Content-Length: %d\r\n\r\n%s", loc, len(body)-1, len(body), len(body), body[:5]), body[5:])
	refusedWhile(t, "a /v0 write while a GCS write is held", v0Probe, 503, "Too many requests in flight", "Retry-After=5 ")
	if resp := finish(); resp.StatusCode != 200 {
		t.Fatalf("the held GCS chunk: %d %v", resp.StatusCode, resp.Header)
	}
	if code, got := v0Probe(); code == 503 {
		t.Fatalf("a /v0 write once the GCS write is done: %d %s", code, got)
	}
}

// hold sends head over a connection of its own and leaves the request's body short; finish sends
// rest and reads the reply.
func hold(t *testing.T, base, head, rest string) (finish func() *http.Response) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := fmt.Fprint(conn, head); err != nil {
		t.Fatal(err)
	}
	return func() *http.Response {
		t.Helper()
		if _, err := fmt.Fprint(conn, rest); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
}

// refusedWhile waits for probe to be refused with status and a reply holding every want: the
// held request takes its place once the server has read its head, a moment after it is sent.
func refusedWhile(t *testing.T, what string, probe func() (int, string), status int, want ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, got := probe()
		if code == status && !slices.ContainsFunc(want, func(w string) bool { return !strings.Contains(got, w) }) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d %s, want %d with %s", what, code, got, status, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// send does req and returns the status and the body, with Retry-After on the front.
func send(t *testing.T, req *http.Request) (int, string) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	_, _ = bufio.NewReader(resp.Body).WriteTo(&b)
	return resp.StatusCode, "Retry-After=" + resp.Header.Get("Retry-After") + " " + b.String()
}
