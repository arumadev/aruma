package gcs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
	"github.com/arumadev/aruma/internal/store/storetest"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// The test bed of the GCS surface: a real core.Service on a fresh Postgres database and a disk
// in a temporary directory, two projects with a bucket each, and a service account token for
// each project. S4-B and S4-C may build on it.

const (
	projectA = 482915736204
	projectB = 582915736204
	bucketA  = "alpha-app.aruma.test"
	bucketB  = "bravo-app.aruma.test"
	// tokenA and tokenB are service account access tokens of project A and project B.
	tokenA  = authn.ServiceTokenPrefix + "alpha"
	tokenB  = authn.ServiceTokenPrefix + "bravo"
	ownerID = "00000000-0000-7000-8000-000000000001"
	emailA  = "backend@alpha-app.iam.aruma.test"
	emailB  = "backend@bravo-app.iam.aruma.test"
	baseURL = "https://aruma.example"
)

type env struct {
	t    *testing.T
	svc  *core.Service
	api  *API
	h    http.Handler
	a, b core.Bucket
}

func newEnv(t *testing.T, tune ...func(*core.Limits)) *env {
	t.Helper()
	pool := storetest.NewPool(t)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO control.accounts (id, email, password_hash) VALUES ('` + ownerID + `', 'o@example.com', 'x')`,
		`INSERT INTO control.projects (project_number, project_id, display_name, owner_account_id, etag, auth_domain)
		 VALUES (482915736204, 'alpha-app', 'Alpha', '` + ownerID + `', '1_a', 'aruma.test'),
		        (582915736204, 'bravo-app', 'Bravo', '` + ownerID + `', '1_b', 'aruma.test')`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := crypto.NewKeys([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	disk, err := blob.NewDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	lim := core.DefaultLimits()
	lim.MinFreeBytes = 1 << 20
	for _, f := range tune {
		f(&lim)
	}
	svc, err := core.New(ctx, pool, disk, core.Options{Limits: lim, Keys: keys, Projects: testProjects{},
		Inflight: rules.NewInflight(1 << 20), DataDir: dir}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	api := New(svc, Options{PublicURL: baseURL + "/", Credentials: testCredentials{}}, slog.New(slog.DiscardHandler))
	return &env{t: t, svc: svc, api: api, h: api.Handler(),
		a: core.Bucket{Project: projectA, Name: bucketA}, b: core.Bucket{Project: projectB, Name: bucketB}}
}

// do sends a request with token as its Bearer ("" for none) and returns the reply.
func (e *env) do(method, target, token string, body io.Reader, header ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(method, target, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

// put stores an object through core, as an admin upload over GCS leaves it.
func (e *env) put(b core.Bucket, name, body string, attrs core.Patch) core.Object {
	e.t.Helper()
	o, err := e.svc.Insert(context.Background(), b, core.Principal{Kind: core.PrincipalAdmin}, core.UploadSpec{
		Name: name, Attrs: attrs, Declared: int64(len(body)), Surface: core.SurfaceGCS,
	}, strings.NewReader(body), int64(len(body)))
	if err != nil {
		e.t.Fatalf("put %s: %v", name, err)
	}
	return o
}

// gcsErr decodes an error reply and checks its shape: code is the status, as a JSON number, and
// errors[] has one entry with the reason.
func gcsErr(t *testing.T, rec *httptest.ResponseRecorder, status int, reason string) wire.GCSErrorBody {
	t.Helper()
	var raw struct {
		Error struct {
			Code json.RawMessage `json:"code"`
		} `json:"error"`
	}
	var body wire.GCSError
	if rec.Code != status || json.Unmarshal(rec.Body.Bytes(), &raw) != nil || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("status %d, want %d: %s", rec.Code, status, rec.Body)
	}
	if string(raw.Error.Code) != strconv.Itoa(status) || len(body.Error.Errors) != 1 || body.Error.Errors[0].Reason != reason ||
		body.Error.Errors[0].Domain != "global" || body.Error.Message == "" {
		t.Fatalf("error body %s, want code %d reason %s", rec.Body, status, reason)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=UTF-8" {
		t.Errorf("Content-Type %q", ct)
	}
	return body.Error
}

// created is when the two projects were made.
var created = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// testProjects is the control plane of the two projects.
type testProjects struct{}

func (testProjects) ProjectForBucket(ctx context.Context, bucket string) (tcore.Project, error) {
	switch bucket {
	case bucketA:
		return tcore.Project{Number: projectA, ID: "alpha-app", State: "ACTIVE", StorageBucket: bucketA, CreatedAt: created}, nil
	case bucketB:
		return tcore.Project{Number: projectB, ID: "bravo-app", State: "ACTIVE", StorageBucket: bucketB, CreatedAt: created}, nil
	}
	return tcore.Project{}, apierr.New(apierr.NotFound, "Requested entity was not found.")
}

func (testProjects) GetProject(ctx context.Context, p *authn.Principal, ref string) (tcore.Project, error) {
	return tcore.Project{}, apierr.New(apierr.NotFound, "Requested entity was not found.")
}

func (testProjects) CompiledRelease(ctx context.Context, project int64, release string) (*rules.Ruleset, error) {
	return nil, errors.New("no rules in these tests")
}

func (testProjects) Audit(ctx context.Context, p *authn.Principal, project int64, action, target string, details map[string]any) error {
	return nil
}

// testCredentials knows the two service accounts' tokens, and nothing else.
type testCredentials struct{}

var errBadToken = apierr.New(apierr.Unauthenticated, "invalid token")

func (testCredentials) VerifySession(context.Context, string) (*authn.Principal, error) {
	return nil, errBadToken
}

func (testCredentials) VerifyAccessToken(context.Context, string) (*authn.Principal, error) {
	return nil, errBadToken
}

func (testCredentials) VerifyServiceToken(_ context.Context, token string) (*authn.Principal, error) {
	switch token {
	case tokenA:
		return &authn.Principal{Kind: authn.KindServiceAccount, Method: authn.MethodServiceToken, ProjectNumber: projectA, Email: emailA}, nil
	case tokenB:
		return &authn.Principal{Kind: authn.KindServiceAccount, Method: authn.MethodServiceToken, ProjectNumber: projectB, Email: emailB}, nil
	}
	return nil, errBadToken
}

// The routing contract (s3-wave-brief): classify sends every request to its route by method,
// query and headers alone, and hands the handler the bucket, the name decoded once, a
// rewrite's destination and a session's id. It looks nothing up and calls no handler, so the
// rows hold whatever the handlers do.
func TestClassify(t *testing.T) {
	const o = "/storage/v1/b/" + bucketA + "/o/dir%2Fa%20b.txt"
	const up = "/upload/storage/v1/b/" + bucketA + "/o"
	cases := []struct {
		method, target string
		header         []string
		route          string // routeNames, or the refusal's status
		status         int
	}{
		// uploadType picks the start.
		{"POST", up + "?uploadType=media&name=a.txt", nil, "upload.media", 0},
		{"POST", up + "?alt=json&name=a.txt&prettyPrint=false&projection=full&uploadType=multipart", nil, "upload.multipart", 0},
		{"POST", up + "?uploadType=multipart", nil, "upload.multipart", 0}, // the name in the JSON part
		{"POST", up + "?name=a.txt&uploadType=resumable", nil, "upload.resumable", 0},
		// Without uploadType, media, or multipart by its header (CT, ADR-023 §3-91).
		{"POST", up + "?name=a.txt", nil, "upload.media", 0},
		{"POST", up, []string{wire.UploadProtocol, "multipart"}, "upload.multipart", 0},
		{"POST", up + "?name=a.txt", []string{wire.UploadProtocol, "resumable"}, "", 400},
		{"POST", up + "?name=a.txt&uploadType=media", []string{wire.UploadProtocol, "multipart"}, "upload.media", 0},
		{"POST", up + "?uploadType=chunked&name=a.txt", nil, "", 400},
		{"PUT", up + "?uploadType=media&name=a.txt", nil, "", 404},
		{"POST", up + "/a.txt?uploadType=media", nil, "", 404},
		{"POST", up + "?uploadType=media&name=a", []string{"X-Goog-Encryption-Key", "k"}, "", 501},
		// An upload_id is a session request whatever the method and uploadType (Go's chunks
		// are POST to a URL with uploadType=resumable).
		{"PUT", up + "?name=a.txt&uploadType=resumable&upload_id=xyz", []string{"Content-Range", "bytes 0-*/*"}, "session", 0},
		{"POST", up + "?name=a.txt&uploadType=resumable&upload_id=xyz", []string{wire.GUploaderNo308, "yes"}, "session", 0},
		{"POST", up + "?upload_id=xyz", nil, "session", 0},
		{"DELETE", up + "?upload_id=xyz", nil, "session", 0},
		{"GET", up + "?upload_id=xyz", nil, "", 404},
		// alt=media under /storage/v1 and /download/storage/v1.
		{"GET", o + "?alt=media", nil, "download", 0},
		{"HEAD", o + "?alt=media&generation=12", nil, "download", 0},
		{"GET", o + "?alt=media&prettyPrint=false&projection=full", []string{"Range", "bytes=0-3"}, "download", 0},
		{"GET", "/download" + o + "?generation=1&alt=media", nil, "download", 0},
		{"PATCH", o + "?alt=media", nil, "", 400},
		{"GET", "/storage/v1/b/" + bucketA + "/o?alt=media", nil, "", 400},
		{"GET", "/download/storage/v1/b/" + bucketA + "/o?alt=media", nil, "", 404},
		{"DELETE", "/download" + o + "?alt=media", nil, "", 404},
		{"GET", o + "?alt=proto", nil, "", 400},
		// The JSON API's own.
		{"GET", o, nil, "objects.get", 0},
		{"GET", o + "?alt=json", nil, "objects.get", 0},
		{"GET", "/download" + o, nil, "objects.get", 0},
		{"PATCH", o, nil, "objects.patch", 0},
		{"DELETE", o, nil, "objects.delete", 0},
		{"POST", o + "/rewriteTo/b/" + bucketB + "/o/x", nil, "objects.rewrite", 0},
		{"GET", "/storage/v1/b/" + bucketA + "/o", nil, "objects.list", 0},
		{"GET", "/storage/v1/b/" + bucketA, nil, "buckets.get", 0},
		{"PATCH", "/storage/v1/b/" + bucketA, nil, "buckets.patch", 0},
		{"PUT", o, nil, "", 501},
		{"GET", "/elsewhere", nil, "", 404},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.target, nil)
		for i := 0; i+1 < len(c.header); i += 2 {
			r.Header.Set(c.header[i], c.header[i+1])
		}
		got := classify(r)
		if c.status != 0 {
			e, ok := errors.AsType[*apiError](got.err)
			if !ok || e.status != c.status || got.route != routeNone {
				t.Errorf("%s %s: %v %v, want %d", c.method, c.target, got.route, got.err, c.status)
			}
			continue
		}
		if got.err != nil || got.route.String() != c.route {
			t.Errorf("%s %s: %v %v, want %s", c.method, c.target, got.route, got.err, c.route)
		}
	}
	// What the handlers get: the bucket, the name decoded once ("+" a plus in a path, a space in
	// a query), the destination still escaped, the session's id.
	for _, c := range []struct {
		method, target string
		want           target
	}{
		{"GET", "/storage/v1/b/" + bucketA + "/o/dir%2Fa+b%20c.txt?alt=media", target{route: routeDownload, bucket: bucketA, object: "dir/a+b c.txt"}},
		{"POST", "/storage/v1/b/" + bucketA + "/o/a%2Fb/rewriteTo/b/" + bucketB + "/o/c%2Fd", target{route: routeRewrite, bucket: bucketA, object: "a/b", dest: "b/" + bucketB + "/o/c%2Fd"}},
		{"POST", "/upload/storage/v1/b/" + bucketA + "/o?uploadType=media&name=dir%2Fa+b.txt", target{route: routeUploadMedia, bucket: bucketA, object: "dir/a b.txt"}},
		{"PUT", "/upload/storage/v1/b/" + bucketA + "/o?name=x&upload_id=id-1", target{route: routeSession, bucket: bucketA, object: "x", uploadID: "id-1"}},
	} {
		if got := classify(httptest.NewRequest(c.method, c.target, nil)); got != c.want {
			t.Errorf("%s %s: %+v, want %+v", c.method, c.target, got, c.want)
		}
	}
}

// The router's own refusals, before any handler: the bucket is resolved first (404 to anyone),
// then the credential (401 without one, 403 for another project's), and a session needs none.
func TestRouterRefusals(t *testing.T) {
	e := newEnv(t)
	const o = "/storage/v1/b/" + bucketA + "/o/a.txt"
	const up = "/upload/storage/v1/b/" + bucketA + "/o"
	for _, c := range []struct {
		method, target, bearer string
		status                 int
		reason                 string
	}{
		{"POST", up + "?uploadType=resumable&name=a.txt", tokenB, 403, "forbidden"},
		{"POST", up + "?uploadType=resumable&name=a.txt", "", 401, "required"},
		{"POST", up + "?uploadType=media&name=a.txt", authn.ServiceTokenPrefix + "nobody", 401, "authError"},
		{"POST", "/upload/storage/v1/b/nobody-app.aruma.test/o?uploadType=media&name=a.txt", tokenA, 404, "notFound"},
		{"PUT", "/upload/storage/v1/b/nobody-app.aruma.test/o?upload_id=xyz", "", 404, "notFound"},
		{"GET", o + "?alt=media", "", 401, "required"},
		{"GET", o + "?alt=media", tokenB, 403, "forbidden"},
		{"GET", "/storage/v1/b/nobody-app.aruma.test/o/x?alt=media", tokenA, 404, "notFound"},
	} {
		gcsErr(t, e.do(c.method, c.target, c.bearer, nil), c.status, c.reason)
	}
}

// upload_requests_per_ip holds every write route, IPv6 per /64, the address from
// X-Forwarded-For of a trusted proxy alone; reads are not counted, and a place is freed when its
// request ends. The refusal is 429 with a Retry-After.
func TestWritesPerIP(t *testing.T) {
	e := newEnv(t, func(l *core.Limits) { l.UploadRequestsPerIP = 1 })
	e.put(e.a, "a.txt", "x", core.Patch{})
	writes := []struct{ method, target, body string }{
		{"PATCH", objA + "a.txt", "{}"},
		{"DELETE", objA + "missing", ""},
		{"POST", objA + "a.txt/rewriteTo/b/" + bucketA + "/o/b.txt", "{}"},
		{"PATCH", "/storage/v1/b/" + bucketA, "{}"},
		{"POST", "/upload/storage/v1/b/" + bucketA + "/o?uploadType=media&name=x", "x"},
		{"POST", "/upload/storage/v1/b/" + bucketA + "/o?uploadType=multipart", "x"},
		{"POST", "/upload/storage/v1/b/" + bucketA + "/o?uploadType=resumable&name=x", "{}"},
		{"PUT", "/upload/storage/v1/b/" + bucketA + "/o?upload_id=x", ""},
	}
	reads := []string{objA + "a.txt", "/storage/v1/b/" + bucketA, "/storage/v1/b/" + bucketA + "/o"}
	run := func(api *API, method, target, body, remote string, header ...string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.RemoteAddr = remote
		r.Header.Set("Authorization", "Bearer "+tokenA)
		for i := 0; i+1 < len(header); i += 2 {
			r.Header.Set(header[i], header[i+1])
		}
		rec := httptest.NewRecorder()
		func() {
			defer func() { _ = recover() }() // a stub of S4-B before its merge
			api.Handler().ServeHTTP(rec, r)
		}()
		return rec
	}
	// One place for 2001:db8::/64, held as by a request in flight.
	release, ok := e.api.writes.Acquire(netip.MustParseAddr("2001:db8::1"), 1)
	if !ok {
		t.Fatal("the first place")
	}
	for _, w := range writes {
		rec := run(e.api, w.method, w.target, w.body, "[2001:db8::99]:4000")
		gcsErr(t, rec, http.StatusTooManyRequests, "rateLimitExceeded")
		if rec.Header().Get("Retry-After") != "1" {
			t.Errorf("%s %s: Retry-After %q", w.method, w.target, rec.Header().Get("Retry-After"))
		}
	}
	for _, target := range reads {
		if rec := run(e.api, "GET", target, "", "[2001:db8::99]:4000"); rec.Code != 200 {
			t.Errorf("a read from the full /64: %d %s", rec.Code, rec.Body)
		}
	}
	// Another /64, and the same /64 once the place is free.
	if rec := run(e.api, "PATCH", objA+"a.txt", "{}", "[2001:db8:0:1::1]:4000"); rec.Code != 200 {
		t.Errorf("another /64: %d %s", rec.Code, rec.Body)
	}
	release()
	if rec := run(e.api, "PATCH", objA+"a.txt", "{}", "[2001:db8::99]:4000"); rec.Code != 200 {
		t.Errorf("after the release: %d %s", rec.Code, rec.Body)
	}
	// Behind a proxy: X-Forwarded-For counts only from a trusted one.
	release, _ = e.api.writes.Acquire(netip.MustParseAddr("198.51.100.7"), 1)
	defer release()
	trusted := New(e.svc, Options{PublicURL: baseURL, Credentials: testCredentials{},
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("192.0.2.0/24")}}, nil)
	trusted.writes = e.api.writes // the same counts
	gcsErr(t, run(trusted, "PATCH", objA+"a.txt", "{}", "192.0.2.10:4000", "X-Forwarded-For", "198.51.100.7"), 429, "rateLimitExceeded")
	if rec := run(trusted, "PATCH", objA+"a.txt", "{}", "192.0.2.10:4000", "X-Forwarded-For", "198.51.100.8"); rec.Code != 200 {
		t.Errorf("another client behind the proxy: %d %s", rec.Code, rec.Body)
	}
	if rec := run(e.api, "PATCH", objA+"a.txt", "{}", "192.0.2.10:4000", "X-Forwarded-For", "198.51.100.7"); rec.Code != 200 {
		t.Errorf("X-Forwarded-For from an untrusted peer counted: %d %s", rec.Code, rec.Body)
	}
}
