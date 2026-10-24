package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
	"github.com/arumadev/aruma/internal/services/storage/wire/wiretest"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

const routeBucket = "chat-app.aruma.test"

// routeProjects resolves routeBucket alone, so the router runs without a database.
type routeProjects struct{ core.Projects }

func (routeProjects) ProjectForBucket(_ context.Context, bucket string) (tcore.Project, error) {
	if bucket != routeBucket {
		return tcore.Project{}, apierr.New(apierr.NotFound, "no such bucket")
	}
	return tcore.Project{Number: 7, State: "ACTIVE", StorageBucket: bucket}, nil
}

// routeService is a core.Service whose pool never connects: what routing needs is the bucket.
func routeService(t *testing.T) *core.Service {
	t.Helper()
	return routeServiceWith(t, core.DefaultLimits())
}

// routeServiceWith is routeService with limits.
func routeServiceWith(t *testing.T, limits core.Limits) *core.Service {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	keys, err := crypto.NewKeys([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := core.New(t.Context(), pool, struct{ blob.Store }{}, core.Options{
		Limits: limits, Keys: keys, Projects: routeProjects{},
		Inflight: rules.NewInflight(1 << 20), DataDir: t.TempDir(),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// serveRecover serves one request and returns the panic of the stub it reached, or "".
func serveRecover(h http.Handler, rec *httptest.ResponseRecorder, req *http.Request) (reached string) {
	defer func() {
		if p := recover(); p != nil {
			reached = fmt.Sprint(p)
		}
	}()
	h.ServeHTTP(rec, req)
	return ""
}

// The routing contract (s3-wave-brief): each request is classified as the handler its method,
// query and headers name, S3-B's and S3-C's included; anything else is production's 400 through
// the whole router, and an unknown bucket production's 404, before any credential.
func TestRoutes(t *testing.T) {
	h := New(routeService(t), Options{}, nil).Handler()
	obj := "/v0/b/" + routeBucket + "/o/photos%2Fa%20b.png"
	list := "/v0/b/" + routeBucket + "/o"
	session := list + "?name=photos%2Fa&upload_id=AbC-1&upload_protocol=resumable"
	for _, c := range []struct {
		method, target string
		header         map[string]string
		want           string // the stub reached, or the status
	}{
		// Reads.
		{"GET", list, nil, "S3-A: list"},
		{"GET", list + "/", nil, "S3-A: list"},
		{"GET", list + "?prefix=photos%2F&delimiter=%2F&maxResults=10&pageToken=x", nil, "S3-A: list"},
		{"GET", "/firebasestorage.googleapis.com" + list, nil, "S3-A: list"},
		{"GET", obj, nil, "S3-A: metadata"},
		{"GET", obj + "?alt=json", nil, "S3-A: metadata"},
		{"HEAD", obj, nil, "S3-A: metadata"},
		{"GET", obj + "?alt=media", nil, "S3-C: download"},
		{"GET", obj + "?alt=media&token=t", map[string]string{"Range": "bytes=0-9"}, "S3-C: download"},
		{"HEAD", obj + "?alt=media", nil, "S3-C: download"},
		{"GET", "/firebasestorage.googleapis.com" + obj + "?alt=media", nil, "S3-C: download"},
		// Metadata writes and tokens.
		{"PATCH", obj, nil, "S3-A: update"},
		{"PUT", obj, map[string]string{"X-HTTP-Method-Override": "PATCH"}, "S3-A: update"},
		{"PUT", obj, map[string]string{"X-HTTP-Method-Override": "patch"}, "S3-A: update"},
		{"DELETE", obj, nil, "S3-A: delete"},
		{"POST", obj + "?create_token=true", nil, "S3-A: tokens"},
		{"POST", obj + "?delete_token=t", nil, "S3-A: tokens"},
		// Uploads (A §3.1): JS multipart, the three resumable starts, a PUT, a media upload.
		{"POST", list + "?name=photos%2Fa", map[string]string{"X-Goog-Upload-Protocol": "multipart",
			"Content-Type": "multipart/related; boundary=b"}, "S3-B: multipart"},
		{"PUT", obj, map[string]string{"X-Goog-Upload-Protocol": "multipart"}, "S3-B: multipart"},
		{"POST", list + "?name=photos%2Fa", map[string]string{"X-Goog-Upload-Protocol": "resumable",
			"X-Goog-Upload-Command": "start", "X-Goog-Upload-Header-Content-Length": "5"}, "S3-B: upload"},
		{"POST", list + "?name=photos%2Fa&uploadType=resumable", map[string]string{"X-Goog-Upload-Protocol": "resumable",
			"X-Goog-Upload-Command": "start"}, "S3-B: upload"},
		{"POST", obj + "?uploadType=resumable&name=photos%2Fa%20b.png", map[string]string{"X-Goog-Upload-Protocol": "resumable",
			"X-Goog-Upload-Command": "start", "X-Goog-Upload-Content-Length": "5"}, "S3-B: upload"},
		{"PUT", list + "?name=photos%2Fa", nil, "S3-B: upload"},
		{"POST", obj, nil, "S3-B: upload"},
		// Session URLs, by POST (JS, Android) and PUT (iOS), with or without a credential.
		{"POST", session, map[string]string{"X-Goog-Upload-Command": "upload, finalize", "X-Goog-Upload-Offset": "0"}, "S3-B: uploadSession"},
		{"PUT", session, map[string]string{"X-Goog-Upload-Command": "query"}, "S3-B: uploadSession"},
		{"POST", session, map[string]string{"X-Goog-Upload-Command": "cancel", "Authorization": "Firebase not-a-token"}, "S3-B: uploadSession"},
		{"PUT", obj + "?upload_id=x", map[string]string{"X-HTTP-Method-Override": "PATCH"}, "S3-B: uploadSession"},
		// Preflights.
		{"OPTIONS", list + "?name=x", map[string]string{"Origin": "https://app.example", "Access-Control-Request-Method": "POST"}, "204"},
		{"OPTIONS", obj, nil, "204"},
		// No operation: production's 400, whatever the bucket.
		{"GET", "/v0/b/", nil, "400"},
		{"GET", "/v0/nothing-here", nil, "400"},
		{"GET", "/v0/", nil, "400"},
		{"GET", "/v0/b/" + routeBucket, nil, "400"},
		{"GET", "/v0/b/" + routeBucket + "/acl", nil, "400"},
		{"GET", "/v0/b/" + routeBucket + "/o/photos/a", nil, "400"},
		{"GET", obj + "?alt=xml", nil, "400"},
		{"GET", session, nil, "400"},
		{"DELETE", session, nil, "400"},
		{"DELETE", list, nil, "400"},
		{"PATCH", list, nil, "400"},
		{"PUT", list, map[string]string{"X-HTTP-Method-Override": "PATCH"}, "400"},
		{"PROPFIND", obj, nil, "400"},
		{"GET", "/v0/b/unknown.aruma.test/o/../x", nil, "400"},
		// An unknown bucket is 404 before any credential, the preflight's too (bucket_probe.json).
		{"GET", "/v0/b/unknown.aruma.test/o?maxResults=1", nil, "404"},
		{"GET", "/v0/b/unknown.aruma.test/o/x", map[string]string{"Authorization": "Firebase not-a-token"}, "404"},
		{"POST", "/v0/b/unknown.aruma.test/o?name=x", map[string]string{"X-Goog-Upload-Protocol": "multipart"}, "404"},
		{"OPTIONS", "/v0/b/unknown.aruma.test/o?name=x", nil, "404"},
		{"POST", "/v0/b/unknown.aruma.test/o?name=x&upload_id=y", nil, "404"},
		// A credential that is not one: 401 (no verifier is configured here).
		{"GET", obj, map[string]string{"Authorization": "Basic YTpi"}, "401"},
		{"GET", obj, map[string]string{"Authorization": "Firebase"}, "401"},
		{"GET", list, map[string]string{"Authorization": "Bearer not-an-admin-token"}, "401"},
		{"DELETE", obj, map[string]string{"Authorization": "Firebase not-a-token"}, "401"},
	} {
		req := httptest.NewRequest(c.method, c.target, nil)
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		if owner, handler, ok := strings.Cut(c.want, ": "); ok && strings.HasPrefix(owner, "S3-") {
			// The handlers run on core, which routeService cannot serve: the route each gets is
			// what is checked here, and what it answers is its owner's tests'
			// (test/acceptance/storage_v0_test.go for S3-A's).
			name := handler
			if handler == "uploadSession" {
				name = routeSession.String()
			}
			rest, _ := wire.CutFirebasePrefix(req.URL.EscapedPath())
			p, err := wire.SplitObjectPath(rest)
			if got := classify(req, p, req.URL.Query()); err != nil || got.String() != name {
				t.Errorf("%s %s %v: route %s, want %s", c.method, c.target, c.header, got, name)
			}
			continue
		}
		rec := httptest.NewRecorder()
		got := serveRecover(h, rec, req)
		if got == "" {
			got = fmt.Sprint(rec.Code)
		}
		if got != c.want {
			t.Errorf("%s %s %v: got %s, want %s: %s", c.method, c.target, c.header, got, c.want, rec.Body)
			continue
		}
		if strings.HasPrefix(got, "S3-") {
			continue
		}
		// Every reply the router itself writes: no null, /v0's error shape, the open CORS.
		wiretest.AssertNoNull(t, rec.Body.Bytes())
		if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s %s: no open CORS: %v", c.method, c.target, rec.Header())
		}
		if rec.Code == http.StatusNoContent {
			continue
		}
		var e wire.FirebaseError
		if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error.Code != rec.Code {
			t.Errorf("%s %s: not /v0's error shape: %s", c.method, c.target, rec.Body)
		}
		want := map[int]string{400: wire.MessageBadMethodOrURL, 404: wire.MessageNotFound}[rec.Code]
		if want != "" && e.Error.Message != want {
			t.Errorf("%s %s: message %q, want %q", c.method, c.target, e.Error.Message, want)
		}
		for k, v := range map[string]string{"Content-Type": "application/json; charset=UTF-8",
			"X-Content-Type-Options": "nosniff", "Cache-Control": "private, max-age=0"} {
			if rec.Header().Get(k) != v {
				t.Errorf("%s %s: %s = %q, want %q", c.method, c.target, k, rec.Header().Get(k), v)
			}
		}
	}
}

// A download is outside the API's open CORS: its CORS is the bucket's, and the router does not
// set * on it, even on its preflight.
func TestRoutesMediaCORS(t *testing.T) {
	a := New(routeService(t), Options{}, nil)
	h := a.Handler()
	obj := "/v0/b/" + routeBucket + "/o/a.png"
	// A download: the router takes it for media, so it skips the open CORS, and mediaCORS sets
	// only Vary without an Origin. download itself is not run: it is S3-C's, on core.
	req := httptest.NewRequest("GET", obj+"?alt=media", nil)
	rest, _ := wire.CutFirebasePrefix(req.URL.EscapedPath())
	p, err := wire.SplitObjectPath(rest)
	if rt := classify(req, p, req.URL.Query()); err != nil || rt != routeDownload || !rt.media(req.URL.Query()) {
		t.Fatalf("GET alt=media: route %s, media %v", rt, rt.media(req.URL.Query()))
	}
	rec := httptest.NewRecorder()
	if err := a.mediaCORS(rec, req, core.Bucket{Project: 7, Name: routeBucket}); err != nil {
		t.Fatal(err)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Vary") != "Origin" {
		t.Errorf("GET alt=media: %v", rec.Header())
	}
	// Its preflight runs through the router, which answers it itself.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("OPTIONS", obj+"?alt=media", nil))
	if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Vary") != "Origin" {
		t.Errorf("OPTIONS alt=media: %d %v", rec.Code, rec.Header())
	}
	// The API's preflight: the methods and headers the SDKs send.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest("OPTIONS", "/v0/b/"+routeBucket+"/o?name=a", nil)
	req.Header.Set("Origin", "https://app.example")
	h.ServeHTTP(rec, req)
	hd := rec.Header()
	if rec.Code != 204 || hd.Get("Access-Control-Allow-Credentials") != "" ||
		!strings.Contains(hd.Get("Access-Control-Allow-Methods"), "PATCH") ||
		!strings.Contains(hd.Get("Access-Control-Allow-Headers"), "X-Goog-Upload-Command") ||
		!strings.Contains(hd.Get("Access-Control-Expose-Headers"), "X-Goog-Upload-URL") {
		t.Errorf("preflight: %d %v", rec.Code, hd)
	}
}

// The name an upload request gives its object: ?name= first, then the path (A §6.8).
func TestUploadName(t *testing.T) {
	for _, c := range []struct{ target, want string }{
		{"/v0/b/x/o?name=a%2Fb", "a/b"},
		{"/v0/b/x/o/a%2Fb?name=a%2Fb", "a/b"},
		{"/v0/b/x/o/p%2Fq", "p/q"},
		{"/v0/b/x/o?name=a+b", "a b"},
		{"/v0/b/x/o?name=a%2Bb", "a+b"},
		{"/v0/b/x/o", ""},
	} {
		u, _ := url.Parse(c.target)
		rest, _ := wire.CutFirebasePrefix(u.EscapedPath())
		p, err := wire.SplitObjectPath(rest)
		if err != nil {
			t.Fatal(err)
		}
		if got := uploadName(p, u.Query()); got != c.want {
			t.Errorf("%s: %q, want %q", c.target, got, c.want)
		}
	}
}

// The router holds upload_requests_per_ip on every write route, before any handler, and on no
// read: with a limit of 1 and one write of the address in flight, each write is refused 503 and
// each read passes; once the write is done its place is free. X-Forwarded-For counts only from a
// trusted proxy.
func TestRouterWritesPerIP(t *testing.T) {
	limits := core.DefaultLimits()
	limits.UploadRequestsPerIP = 1
	svc := routeServiceWith(t, limits)
	a := New(svc, Options{}, nil)
	h := a.Handler()
	obj := "/v0/b/" + routeBucket + "/o/a.png"
	list := "/v0/b/" + routeBucket + "/o"
	session := list + "?name=a.png&upload_id=x"
	refused := func(h http.Handler, method, target string, header map[string]string) (bool, string) {
		req := httptest.NewRequest(method, target, nil) // from 192.0.2.1
		for k, v := range header {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		reached := serveRecover(h, rec, req)
		return reached == "" && rec.Code == 503 && strings.Contains(rec.Body.String(), "Too many requests in flight"),
			reached + " " + rec.Body.String()
	}

	release, ok := a.writes.Acquire(netip.MustParseAddr("192.0.2.1"), 1) // the write held open
	if !ok {
		t.Fatal("acquire")
	}
	for _, c := range []struct {
		method, target string
		header         map[string]string
	}{
		{"PATCH", obj, nil},
		{"PUT", obj, map[string]string{"X-HTTP-Method-Override": "PATCH"}},
		{"DELETE", obj, nil},
		{"POST", obj + "?create_token=true", nil},
		{"POST", obj + "?delete_token=t", nil},
		{"POST", list + "?name=a.png", map[string]string{"X-Goog-Upload-Protocol": "multipart"}},
		{"POST", list + "?name=a.png", map[string]string{"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start"}},
		{"PUT", obj, nil},
		{"POST", session, map[string]string{"X-Goog-Upload-Command": "upload, finalize"}},
		{"PUT", session, map[string]string{"X-Goog-Upload-Command": "query"}},
		// An X-Forwarded-For from a peer that is not a trusted proxy is not believed.
		{"POST", obj + "?create_token=true", map[string]string{"X-Forwarded-For": "198.51.100.7"}},
	} {
		if ok, got := refused(h, c.method, c.target, c.header); !ok {
			t.Errorf("%s %s %v: not refused: %s", c.method, c.target, c.header, got)
		}
	}
	for _, c := range []struct{ method, target string }{
		{"GET", list}, {"GET", obj}, {"HEAD", obj}, {"OPTIONS", obj},
	} {
		if ok, got := refused(h, c.method, c.target, nil); ok {
			t.Errorf("%s %s: a read refused: %s", c.method, c.target, got)
		}
	}
	// Behind a trusted proxy, the client is the one X-Forwarded-For names.
	trusted := New(svc, Options{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}, nil)
	trusted.writes.Acquire(netip.MustParseAddr("192.0.2.1"), 1)
	if ok, got := refused(trusted.Handler(), "POST", obj+"?create_token=true", map[string]string{"X-Forwarded-For": "198.51.100.7"}); ok {
		t.Errorf("a trusted proxy's client counted as the proxy: %s", got)
	}

	// The held write ends; a write runs to its end (an anonymous create_token, 403) and frees its
	// place for the next.
	release()
	for range 2 {
		if ok, got := refused(h, "POST", obj+"?create_token=true", nil); ok || !strings.Contains(got, "Permission denied.") {
			t.Errorf("a write after the release: %s", got)
		}
	}
}
