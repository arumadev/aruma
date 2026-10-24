package api

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
	"github.com/arumadev/aruma/internal/services/storage/wire/wiretest"
)

// What an upload's JSON becomes (A §3.9, §3.10, §7): the bodies the three SDKs send at start and
// in a multipart, iOS's nulls and empty bucket, Android's empty body, and the client's attempts
// at the tokens.
func TestParseResource(t *testing.T) {
	s := func(v string) *string { return &v }
	sum := [16]byte{0x1c, 0xa5, 0x6c, 0xd5, 0xf6, 0x95, 0x2b, 0xf3, 0x90, 0x42, 0x20, 0x0d, 0x6e, 0x1b, 0x8e, 0x55}
	md5 := base64.StdEncoding.EncodeToString(sum[:])
	for _, c := range []struct {
		name, body string
		fields     map[string]*string
		metadata   map[string]*string
		objectName string
		md5        bool
		bad        bool
	}{
		{name: "Android: no body"},
		{name: "empty object", body: "{}"},
		{name: "JS start", body: `{"name":"a/b.txt","fullPath":"a/b.txt","contentType":"text/plain","metadata":{"big":"yes"}}`,
			fields: map[string]*string{"contentType": s("text/plain")}, metadata: map[string]*string{"big": s("yes")}, objectName: "a/b.txt"},
		{name: "iOS start: an empty bucket, metadata null, nulls for the rest",
			body:   `{"name":"a.png","bucket":"","contentType":"image/png","metadata":null,"cacheControl":null,"size":3}`,
			fields: map[string]*string{"contentType": s("image/png")}, objectName: "a.png"},
		{name: "every content field", body: `{"cacheControl":"c","contentDisposition":"d","contentEncoding":"e","contentLanguage":"f","contentType":"g"}`,
			fields: map[string]*string{"cacheControl": s("c"), "contentDisposition": s("d"), "contentEncoding": s("e"),
				"contentLanguage": s("f"), "contentType": s("g")}},
		{name: "a planted token is dropped", body: `{"metadata":{"firebaseStorageDownloadTokens":"mine","k":"v","n":null}}`,
			metadata: map[string]*string{"k": s("v")}},
		{name: "downloadTokens is ignored", body: `{"downloadTokens":"mine","crc32c":"AAAAAA==","generation":"1"}`},
		{name: "md5Hash", body: `{"md5Hash":"` + md5 + `"}`, md5: true},
		{name: "md5Hash null", body: `{"md5Hash":null}`},
		{name: "md5Hash not base64", body: `{"md5Hash":"not base64"}`, bad: true},
		{name: "md5Hash of 15 bytes", body: `{"md5Hash":"` + base64.StdEncoding.EncodeToString(sum[:15]) + `"}`, bad: true},
		{name: "a number for a field", body: `{"contentType":5}`, bad: true},
		{name: "a number in the metadata", body: `{"metadata":{"k":5}}`, bad: true},
		{name: "a name that is no string", body: `{"name":["a"]}`, bad: true},
		{name: "not an object", body: `[]`, bad: true},
		{name: "null", body: `null`, bad: true},
		{name: "not JSON", body: `name=a`, bad: true},
	} {
		got, err := parseResource([]byte(c.body))
		if c.bad {
			if e, ok := errors.AsType[*httpError](err); !ok || e.status != http.StatusBadRequest {
				t.Errorf("%s: %+v, %v, want 400", c.name, got, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !sameFields(got.attrs.Fields, c.fields) || !sameFields(got.attrs.Metadata, c.metadata) ||
			got.attrs.ClearMetadata || got.attrs.Tokens != nil || got.name != c.objectName {
			t.Errorf("%s: %s name %q", c.name, describePatch(got.attrs), got.name)
		}
		if (got.want.MD5 != nil) != c.md5 || c.md5 && *got.want.MD5 != sum || got.want.CRC32C != nil {
			t.Errorf("%s: checksums %+v", c.name, got.want)
		}
	}
}

// The reply on a session URL for each state core gives (ADR-022 §6.1): final on every terminal
// refusal and on a cancelled session's chunk, active on a 2xx of a live session, a gap and a
// 503 alone, and no status on an unknown session or a malformed request.
func TestWriteSession(t *testing.T) {
	a := New(routeService(t), Options{}, nil)
	obj := &core.Object{Bucket: routeBucket, Name: "a.txt", Generation: 7, Metageneration: 1, Size: 3,
		DownloadTokens: []string{"t"}, Created: time.Unix(1, 0), Updated: time.Unix(1, 0)}
	active := core.UploadState{Status: core.UploadActive, Received: 10}
	for _, c := range []struct {
		name     string
		st       core.UploadState
		err      error
		code     int
		status   string // X-Goog-Upload-Status, "" when absent
		received string // X-Goog-Upload-Size-Received, "" when absent
		object   bool
	}{
		{"a chunk", active, nil, 200, "active", "10", false},
		{"the finalize", core.UploadState{Status: core.UploadFinal, Received: 3, Object: obj}, nil, 200, "final", "3", true},
		{"a gap", active, &core.OffsetError{Received: 10}, 400, "active", "10", false},
		{"a short body", active, &core.InvalidError{Reason: "invalid", Message: "The request body is shorter than its length."}, 400, "active", "10", false},
		{"the disk floor", active, fmt.Errorf("%w: disk", core.ErrUnavailable), 503, "active", "10", false},
		{"the rules engine", core.UploadState{}, core.ErrOverloaded, 503, "active", "", false},
		{"the rules", core.UploadState{Status: core.UploadFinal, Received: 10, Err: core.ErrPermissionDenied}, core.ErrPermissionDenied, 403, "final", "10", false},
		{"the quota", core.UploadState{Status: core.UploadFinal, Received: 10, Err: core.ErrQuotaExceeded}, core.ErrQuotaExceeded, 402, "final", "10", false},
		{"md5Hash", core.UploadState{Status: core.UploadFinal, Received: 3, Err: core.ErrChecksumMismatch}, core.ErrChecksumMismatch, 400, "final", "3", false},
		{"the declared size", core.UploadState{Status: core.UploadFinal, Received: 4, Err: core.ErrSizeMismatch}, core.ErrSizeMismatch, 400, "final", "4", false},
		{"object_bytes", core.UploadState{Status: core.UploadFinal, Received: 4, Err: core.ErrTooLarge}, core.ErrTooLarge, 413, "final", "4", false},
		{"a user disabled since the start", core.UploadState{Status: core.UploadFinal, Err: core.ErrUnauthenticated}, core.ErrUnauthenticated, 401, "final", "0", false},
		{"a chunk on a cancelled session", core.UploadState{Status: core.UploadCancelled, Received: 2}, core.ErrUploadCancelled, 400, "final", "2", false},
		{"a cancel after final", core.UploadState{Status: core.UploadFinal, Received: 3, Object: obj}, core.ErrUploadFinal, 400, "final", "3", false},
		{"an unknown session", core.UploadState{}, core.ErrUploadNotFound, 404, "", "", false},
	} {
		rec := httptest.NewRecorder()
		a.writeSession(rec, httptest.NewRequest("POST", "/v0/b/x/o?upload_id=y", nil), c.st, c.err)
		h := rec.Header()
		if rec.Code != c.code || headerExact(h, wire.UploadStatus) != c.status || headerExact(h, wire.UploadSizeReceived) != c.received {
			t.Errorf("%s: %d %q %q, want %d %q %q", c.name, rec.Code, headerExact(h, wire.UploadStatus),
				headerExact(h, wire.UploadSizeReceived), c.code, c.status, c.received)
		}
		wiretest.AssertNoNull(t, rec.Body.Bytes())
		if got := strings.Contains(rec.Body.String(), `"downloadTokens":"t"`); got != c.object {
			t.Errorf("%s: body %s", c.name, rec.Body)
		}
		if c.code == 200 && !c.object && rec.Body.Len() != 0 {
			t.Errorf("%s: a body on a reply that has none: %s", c.name, rec.Body)
		}
		// Retry-After on pressure alone: with it iOS retries without its limits (A §5.3), so a
		// server that is down must not send it.
		if ra := h.Get("Retry-After"); (ra != "") != (c.err == core.ErrOverloaded) {
			t.Errorf("%s: Retry-After %q", c.name, ra)
		}
	}
}

// headerExact reads a header under Google's spelling alone: wire.Set writes it so (A §6.2).
func headerExact(h http.Header, name string) string {
	if v := h[name]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// What the upload handlers refuse before core is asked: an unknown or misplaced command, an
// unknown protocol, a malformed offset or declared size, an upload that names no object. None
// carries X-Goog-Upload-Status (the client has no session to resume).
func TestUploadRefusals(t *testing.T) {
	h := New(routeService(t), Options{}, nil).Handler()
	list := "/v0/b/" + routeBucket + "/o"
	session := list + "?name=a&upload_id=x&upload_protocol=resumable"
	start := map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start"}
	with := func(m map[string]string, k, v string) map[string]string {
		out := map[string]string{k: v}
		for k, v := range m {
			if _, ok := out[k]; !ok {
				out[k] = v
			}
		}
		return out
	}
	for _, c := range []struct {
		name, method, target string
		header               map[string]string
		body                 string
		code                 int
	}{
		{"an unknown command", "POST", list + "?name=a", with(start, "X-Goog-Upload-Command", "bogus"), "", 400},
		{"resumable without a command", "POST", list + "?name=a", map[string]string{"X-Goog-Upload-Protocol": "resumable"}, "", 400},
		{"upload without a session", "POST", list + "?name=a", with(start, "X-Goog-Upload-Command", "upload, finalize"), "", 400},
		{"an unknown protocol", "POST", list + "?name=a", map[string]string{"X-Goog-Upload-Protocol": "raw"}, "x", 400},
		{"a start without a name", "POST", list, start, "{}", 400},
		{"a start whose name is in the body alone", "POST", list, start, `{"name":"a"}`, 400},
		{"a malformed declared size", "POST", list + "?name=a", with(start, "X-Goog-Upload-Header-Content-Length", "-1"), "", 400},
		{"a start body that is not JSON", "POST", list + "?name=a", start, "name=a", 400},
		{"a start body over json_body_bytes", "POST", list + "?name=a", start,
			`{"metadata":{"k":"` + strings.Repeat("x", int(core.DefaultLimits().JSONBodyBytes)) + `"}}`, 413},
		{"a media upload without a name", "POST", list, map[string]string{"Content-Type": "text/plain"}, "x", 400},
		{"a multipart without a name", "POST", list, map[string]string{"X-Goog-Upload-Protocol": "multipart",
			"Content-Type": "multipart/related; boundary=b"},
			"--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--b\r\nContent-Type: text/plain\r\n\r\nx\r\n--b--", 400},
		{"a multipart with a wrong boundary", "POST", list + "?name=a", map[string]string{"X-Goog-Upload-Protocol": "multipart",
			"Content-Type": "multipart/related; boundary=other"},
			"--b\r\nContent-Type: application/json\r\n\r\n{}\r\n--b\r\nContent-Type: text/plain\r\n\r\nx\r\n--b--", 400},
		{"a session without a command", "POST", session, nil, "", 400},
		{"start on a session", "PUT", session, map[string]string{"X-Goog-Upload-Command": "start"}, "", 400},
		{"query and upload together", "POST", session, map[string]string{"X-Goog-Upload-Command": "query, upload"}, "", 400},
		{"an upload without an offset", "POST", session, map[string]string{"X-Goog-Upload-Command": "upload"}, "abc", 400},
		{"a signed offset", "POST", session, map[string]string{"X-Goog-Upload-Command": "upload", "X-Goog-Upload-Offset": "+3"}, "abc", 400},
	} {
		req := httptest.NewRequest(c.method, c.target, strings.NewReader(c.body))
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		if reached := serveRecover(h, rec, req); reached != "" {
			t.Errorf("%s: reached %s", c.name, reached)
			continue
		}
		if rec.Code != c.code || headerExact(rec.Header(), wire.UploadStatus) != "" {
			t.Errorf("%s: %d %v, want %d without a status: %s", c.name, rec.Code, rec.Header(), c.code, rec.Body)
		}
		wiretest.AssertNoNull(t, rec.Body.Bytes())
	}
}

// pacedServer serves h behind the server's real timeouts scaled down: ReadTimeout and
// WriteTimeout of one second, and pace windows of 200 ms.
func pacedServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	old := paceWindow
	paceWindow = 200 * time.Millisecond
	t.Cleanup(func() { paceWindow = old })
	ts := httptest.NewUnstartedServer(h)
	ts.Config.ReadTimeout = time.Second
	ts.Config.WriteTimeout = time.Second
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

// pacedHandler reads the body as an upload handler does, takes commit to commit after it, and
// answers how many bytes it read, or 408 when the body was too slow.
func pacedHandler(a *API, commit time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body := a.pace(w, r)
		n, _ := io.Copy(io.Discard, body)
		time.Sleep(commit)
		body.replying()
		if body.slow() {
			a.writeError(w, r, errTooSlow)
			return
		}
		fmt.Fprint(w, n)
	}
}

// An upload longer than the server's timeouts gets its reply, a commit that outlasts the last
// window included (ADR-022 §6.4): the deadlines move with the bytes, the write's set once more
// before the reply. The default rate is 4 KiB/s, so a window of 200 ms needs 4 KiB.
func TestPacedLongUpload(t *testing.T) {
	a := New(routeService(t), Options{}, nil)
	ts := pacedServer(t, pacedHandler(a, 700*time.Millisecond))
	pr, pw := io.Pipe()
	go func() {
		chunk := strings.Repeat("x", 8<<10)
		for range 30 { // 240 KiB over three seconds
			if _, err := io.WriteString(pw, chunk); err != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		pw.Close()
	}()
	start := time.Now()
	res, err := http.Post(ts.URL, "application/octet-stream", pr)
	if err != nil {
		t.Fatalf("after %v: %v", time.Since(start), err)
	}
	defer res.Body.Close()
	got, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || string(got) != fmt.Sprint(30*8<<10) || time.Since(start) < 3*time.Second {
		t.Fatalf("after %v: %d %s", time.Since(start), res.StatusCode, got)
	}
}

// A slowloris is cut at the end of its first window short of the rate, and told so; a client
// that sends nothing at all is cut the same way.
func TestPacedSlowloris(t *testing.T) {
	a := New(routeService(t), Options{}, nil)
	ts := pacedServer(t, pacedHandler(a, 0))
	for _, sent := range []string{"", "abc"} {
		conn, err := net.Dial("tcp", ts.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 1000000\r\n\r\n%s", sent)
		start := time.Now()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		res, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("%q: %v", sent, err)
		}
		if res.StatusCode != http.StatusRequestTimeout || time.Since(start) > 900*time.Millisecond {
			t.Errorf("%q: %d after %v", sent, res.StatusCode, time.Since(start))
		}
		conn.Close()
	}
}

// A chunk on a finished session is a repeat when it brings nothing past the bytes received, by
// its length or, without one, by its body (ADR-023 §3-86).
func TestPastReceived(t *testing.T) {
	for _, c := range []struct {
		offset, length int64
		body           string
		want           bool
	}{
		{15, 5, "fghij", false}, {20, 0, "", false}, {0, 20, "", false}, {20, 1, "z", true}, {18, 5, "ijklm", true},
		{15, -1, "fghij", false}, {20, -1, "", false}, {15, -1, "fghijk", true}, {25, -1, "", false}, {25, -1, "x", true},
	} {
		if got := pastReceived(strings.NewReader(c.body), c.offset, c.length, 20); got != c.want {
			t.Errorf("%d+%d %q: %v, want %v", c.offset, c.length, c.body, got, c.want)
		}
	}
}
