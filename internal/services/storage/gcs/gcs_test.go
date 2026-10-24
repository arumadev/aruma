package gcs

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func newEnv(t *testing.T) *env {
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
	if string(raw.Error.Code) != itoa(status) || len(body.Error.Errors) != 1 || body.Error.Errors[0].Reason != reason ||
		body.Error.Errors[0].Domain != "global" || body.Error.Message == "" {
		t.Fatalf("error body %s, want code %d reason %s", rec.Body, status, reason)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=UTF-8" {
		t.Errorf("Content-Type %q", ct)
	}
	return body.Error
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// testProjects is the control plane of the two projects.
type testProjects struct{}

func (testProjects) ProjectForBucket(ctx context.Context, bucket string) (tcore.Project, error) {
	switch bucket {
	case bucketA:
		return tcore.Project{Number: projectA, ID: "alpha-app", State: "ACTIVE", StorageBucket: bucketA}, nil
	case bucketB:
		return tcore.Project{Number: projectB, ID: "bravo-app", State: "ACTIVE", StorageBucket: bucketB}, nil
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

// reached runs the handler on r and returns the message of the stub it panicked in, "" when it
// answered without one.
func (e *env) reached(r *http.Request) (stub string, rec *httptest.ResponseRecorder) {
	rec = httptest.NewRecorder()
	defer func() {
		if p := recover(); p != nil {
			s, ok := p.(string)
			if !ok {
				panic(p)
			}
			stub = s
		}
	}()
	e.h.ServeHTTP(rec, r)
	return "", rec
}

// The routing contract (s3-wave-brief): every upload and download request reaches its stub in
// upload.go or download.go, by method, query and headers, and nothing else does.
func TestRoutingReachesTheStubs(t *testing.T) {
	e := newEnv(t)
	const o = "/storage/v1/b/" + bucketA + "/o/dir%2Fa%20b.txt"
	const up = "/upload/storage/v1/b/" + bucketA + "/o"
	cases := []struct {
		method, target string
		bearer         string
		header         []string
		want           string // the stub's panic, or "" for a reply
		status         int    // the reply's status when want is "", 0 for any
	}{
		// uploadType picks the start; the Bearer must be the bucket's project's.
		{"POST", up + "?uploadType=media&name=a.txt", tokenA, nil, "S4-B: uploadMedia", 0},
		{"POST", up + "?alt=json&name=a.txt&prettyPrint=false&projection=full&uploadType=multipart", tokenA, nil, "S4-B: uploadMultipart", 0},
		{"POST", up + "?uploadType=multipart", tokenA, nil, "S4-B: uploadMultipart", 0}, // the name in the JSON part
		{"POST", up + "?name=a.txt&uploadType=resumable", tokenA, nil, "S4-B: startResumable", 0},
		{"POST", up + "?uploadType=resumable&name=a.txt", tokenB, nil, "", 403},
		{"POST", up + "?uploadType=resumable&name=a.txt", "", nil, "", 401},
		{"POST", up + "?uploadType=media&name=a.txt", authn.ServiceTokenPrefix + "nobody", nil, "", 401},
		{"POST", up + "?name=a.txt", tokenA, nil, "", 400},
		{"POST", up + "?uploadType=chunked&name=a.txt", tokenA, nil, "", 400},
		{"PUT", up + "?uploadType=media&name=a.txt", tokenA, nil, "", 404},
		{"POST", "/upload/storage/v1/b/nobody-app.aruma.test/o?uploadType=media&name=a.txt", tokenA, nil, "", 404},
		{"POST", up + "/a.txt?uploadType=media", tokenA, nil, "", 404},
		// An upload_id is a session request whatever the method and uploadType, without a
		// Bearer, and with anyone's (Go's chunks are POST to a URL with uploadType=resumable).
		{"PUT", up + "?name=a.txt&uploadType=resumable&upload_id=xyz", "", []string{"Content-Range", "bytes 0-*/*"}, "S4-B: resumeUpload", 0},
		{"POST", up + "?name=a.txt&uploadType=resumable&upload_id=xyz", "", []string{wire.GUploaderNo308, "yes"}, "S4-B: resumeUpload", 0},
		{"POST", up + "?upload_id=xyz", tokenB, nil, "S4-B: resumeUpload", 0},
		{"DELETE", up + "?upload_id=xyz", "", nil, "S4-B: resumeUpload", 0},
		{"GET", up + "?upload_id=xyz", "", nil, "", 404},
		{"PUT", "/upload/storage/v1/b/nobody-app.aruma.test/o?upload_id=xyz", "", nil, "", 404},
		// alt=media under /storage/v1 and /download/storage/v1.
		{"GET", o + "?alt=media", tokenA, nil, "S4-C: download", 0},
		{"HEAD", o + "?alt=media&generation=12", tokenA, nil, "S4-C: download", 0},
		{"GET", o + "?alt=media&prettyPrint=false&projection=full", tokenA, []string{"Range", "bytes=0-3"}, "S4-C: download", 0},
		{"GET", "/download" + o + "?generation=1&alt=media", tokenA, nil, "S4-C: download", 0},
		{"GET", o + "?alt=media", "", nil, "", 401},
		{"GET", o + "?alt=media", tokenB, nil, "", 403},
		{"GET", "/storage/v1/b/nobody-app.aruma.test/o/x?alt=media", tokenA, nil, "", 404},
		{"PATCH", o + "?alt=media", tokenA, nil, "", 400},
		{"GET", "/storage/v1/b/" + bucketA + "/o?alt=media", tokenA, nil, "", 400},
		{"GET", "/download/storage/v1/b/" + bucketA + "/o?alt=media", tokenA, nil, "", 404},
		{"DELETE", "/download" + o + "?alt=media", tokenA, nil, "", 404},
		{"GET", o + "?alt=proto", tokenA, nil, "", 400},
		// The JSON API's own requests reach no stub.
		{"GET", o, tokenA, nil, "", 0},               // objects.get
		{"GET", o + "?alt=json", tokenA, nil, "", 0}, // objects.get
		{"GET", "/download" + o, tokenA, nil, "", 0}, // objects.get
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.target, strings.NewReader(""))
		if c.bearer != "" {
			r.Header.Set("Authorization", "Bearer "+c.bearer)
		}
		for i := 0; i+1 < len(c.header); i += 2 {
			r.Header.Set(c.header[i], c.header[i+1])
		}
		stub, rec := e.reached(r)
		switch {
		case stub != c.want:
			t.Errorf("%s %s: reached %q, want %q", c.method, c.target, stub, c.want)
		case c.want == "" && c.status != 0 && rec.Code != c.status:
			t.Errorf("%s %s: %d, want %d: %s", c.method, c.target, rec.Code, c.status, rec.Body)
		}
	}
}

// The router hands the stubs the bucket, the name decoded once, and the principal; a session
// request also gets its id.
func TestRoutingHandsTheStubsTheirArguments(t *testing.T) {
	e := newEnv(t)
	type call struct {
		b          core.Bucket
		name, id   string
		p          core.Principal
		uploadType string
	}
	var got call
	record := func(w http.ResponseWriter, r *http.Request, b core.Bucket, name string, p core.Principal) {
		got = call{b: b, name: name, p: p, uploadType: r.URL.Query().Get("uploadType")}
	}
	// The routes as routeJSON and routeUpload resolve them, on a test double of each stub.
	r := httptest.NewRequest("GET", "/storage/v1/b/"+bucketA+"/o/dir%2Fa+b%20c.txt?alt=media", nil)
	r.Header.Set("Authorization", "Bearer "+tokenA)
	e.api.serveObject(httptest.NewRecorder(), r, bucketA, "dir/a+b c.txt", permObjectsGet, record)
	want := core.Principal{Kind: core.PrincipalAdmin, ServiceAccount: emailA}
	if got.b != e.a || got.name != "dir/a+b c.txt" || got.p.Kind != want.Kind || got.p.ServiceAccount != want.ServiceAccount {
		t.Fatalf("download got %+v", got)
	}
	path, err := wire.SplitObjectPath(strings.TrimPrefix("/storage/v1/b/"+bucketA+"/o/dir%2Fa+b%20c.txt", wire.GCSPrefix))
	if err != nil || path.Object != "dir/a+b c.txt" || path.Bucket != bucketA {
		t.Fatalf("the path decodes to %+v, %v", path, err)
	}
	// An upload's name comes from the query with form rules: "+" is a space there.
	q := httptest.NewRequest("POST", "/upload/storage/v1/b/"+bucketA+"/o?uploadType=media&name=dir%2Fa+b.txt", nil).URL.Query()
	if q.Get("name") != "dir/a b.txt" {
		t.Fatalf("query name %q", q.Get("name"))
	}
}
