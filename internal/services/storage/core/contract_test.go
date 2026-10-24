package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// The tests of the contract commit (S2-0): the exported API is pinned, and the parts that work
// from the contract (helpers, errors, limits, Bucket, New, authorize) are tested. Each task tests
// its own behaviour in its own files.

// The signatures of the contract: a change does not compile.
var (
	_ func(context.Context, *pgxpool.Pool, blob.Store, Options, *slog.Logger) (*Service, error)                         = New
	_ func(*Service) Limits                                                                                             = (*Service).Limits
	_ func(*Service) time.Time                                                                                          = (*Service).Now
	_ func(*Service, context.Context, string) (Bucket, error)                                                           = (*Service).Bucket
	_ func(*Service, context.Context, Bucket, string, Principal, Surface, int64) (Object, error)                        = (*Service).Stat
	_ func(*Service, context.Context, Bucket, string, Principal, string, int64) (Object, blob.Reader, error)            = (*Service).Open
	_ func(*Service, context.Context, Bucket, Principal, ListOptions) (ListPage, error)                                 = (*Service).List
	_ func(*Service, context.Context, Bucket, string, Principal, Surface, Patch, Preconditions) (Object, error)         = (*Service).Update
	_ func(*Service, context.Context, Bucket, string, Principal, Preconditions) error                                   = (*Service).Delete
	_ func(*Service, context.Context, Bucket, string, Bucket, string, Principal, *Patch, Preconditions) (Object, error) = (*Service).Rewrite
	_ func(*Service, context.Context, Bucket, string, Principal) (Object, error)                                        = (*Service).CreateToken
	_ func(*Service, context.Context, Bucket, string, Principal, string) (Object, error)                                = (*Service).DeleteToken
	_ func(*Service, context.Context, Bucket) ([]CORSRule, error)                                                       = (*Service).CORS
	_ func(*Service, context.Context, Bucket, Principal, []CORSRule) error                                              = (*Service).SetCORS
	_ func(*Service, []CORSRule, string, string) (bool, CORSRule)                                                       = (*Service).MatchCORS
	_ func(*Service, context.Context, Bucket) (Usage, error)                                                            = (*Service).Usage
	_ func(*Service, context.Context, Bucket, Principal, UploadSpec, io.Reader, int64) (Object, error)                  = (*Service).Insert
	_ func(*Service, context.Context, Bucket, Principal, UploadSpec) (string, error)                                    = (*Service).StartUpload
	_ func(*Service, context.Context, Bucket, string, string, *Principal, Chunk) (UploadState, error)                   = (*Service).WriteUpload
	_ func(*Service, context.Context, Bucket, string, string) (UploadState, error)                                      = (*Service).QueryUpload
	_ func(*Service, context.Context, Bucket, string, string) (UploadState, error)                                      = (*Service).CancelUpload
	_ func(*Service, context.Context, int64) error                                                                      = (*Service).PurgeProject
	_ func(*Service, context.Context) error                                                                             = (*Service).Boot
	_ func(*Service, context.Context) error                                                                             = (*Service).Run
	_ func(*Service, context.Context, objectWrite) (Object, error)                                                      = (*Service).commitObject
	_ func(*Service, context.Context, Bucket, Principal, ruleOp) error                                                  = (*Service).authorize
	_ func(string) error                                                                                                = validateName
	_ func(string) []string                                                                                             = segments
	_ func(*Object, Surface)                                                                                            = applyDefaults
	_ func(string) string                                                                                               = defaultDisposition
	_ func(map[string]*string)                                                                                          = StripClientTokens
	_ func() string                                                                                                     = newDownloadToken
	_ func(*Service, int64, string, []string) ([]byte, error)                                                           = (*Service).sealTokens
	_ func(*Service, int64, string, []byte) ([]string, error)                                                           = (*Service).openTokens
)

// No task adds an exported method to Service: the surfaces are written against this list.
func TestServiceMethodsArePinned(t *testing.T) {
	want := []string{"Boot", "Bucket", "CORS", "CancelUpload", "CreateToken", "Delete", "DeleteToken", "Insert",
		"Limits", "List", "MatchCORS", "Now", "Open", "PurgeProject", "QueryUpload", "Rewrite", "Run", "SetCORS",
		"StartUpload", "Stat", "Update", "Usage", "WriteUpload"}
	typ := reflect.TypeFor[*Service]()
	var got []string
	for i := range typ.NumMethod() {
		got = append(got, typ.Method(i).Name)
	}
	if !slices.Equal(got, want) {
		t.Errorf("Service's exported methods are %v, want %v", got, want)
	}
}

// The exported types' fields, pinned by name and type.
func TestTypesArePinned(t *testing.T) {
	for _, c := range []struct {
		v    any
		want string
	}{
		{Principal{}, "Kind PrincipalKind; UID string; Token map[string]interface {}; AuthTime time.Time; IssuedAt time.Time; ServiceAccount string; ConsoleAccount string"},
		{Bucket{}, "Project int64; Name string"},
		{Object{}, "Bucket string; Name string; Generation int64; Metageneration int64; Size int64; MD5 [16]uint8; CRC32C uint32; " +
			"ContentType string; ContentEncoding string; ContentDisposition string; ContentLanguage string; CacheControl string; " +
			"Metadata map[string]string; DownloadTokens []string; Created time.Time; Updated time.Time"},
		{Patch{}, "Fields map[string]*string; Metadata map[string]*string; ClearMetadata bool; Tokens *[]string"},
		{Preconditions{}, "IfGenerationMatch *int64; IfGenerationNotMatch *int64; IfMetagenerationMatch *int64; IfMetagenerationNotMatch *int64"},
		{Checksums{}, "CRC32C *uint32; MD5 *[16]uint8"},
		{UploadSpec{}, "Name string; Attrs core.Patch; Declared int64; Surface core.Surface; Pre core.Preconditions; Want core.Checksums; IP netip.Addr"},
		{Chunk{}, "Offset int64; Body io.Reader; Length int64; Finalize bool; Total int64"},
		{UploadState{}, "Status core.UploadStatus; Received int64; Object *core.Object; Err error"},
		{ListOptions{}, "Prefix string; Delimiter string; PageToken string; StartOffset string; EndOffset string; MaxResults int; Surface core.Surface"},
		{ListPage{}, "Prefixes []string; Objects []core.Object; NextPageToken string"},
		{CORSRule{}, "Origins []string; Methods []string; ResponseHeaders []string; MaxAgeSeconds int"},
		{Usage{}, "UsedBytes int64; ReservedBytes int64; Objects int64; QuotaBytes int64"},
		{InvalidError{}, "Reason string; Message string"},
		{OffsetError{}, "Received int64"},
	} {
		if got := strings.ReplaceAll(fields(c.v), "core.PrincipalKind", "PrincipalKind"); got != c.want {
			t.Errorf("%T:\n got %s\nwant %s", c.v, got, c.want)
		}
	}
}

func fields(v any) string {
	typ := reflect.TypeOf(v)
	var parts []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		parts = append(parts, f.Name+" "+f.Type.String())
	}
	return strings.Join(parts, "; ")
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"a", "photos/a b.png", "a//b", "/x", "naïve 'q' 1+1%;x.txt", ".well-known/x", strings.Repeat("é", 512), "..a", "a/.."} {
		if err := validateName(ok); err != nil {
			t.Errorf("validateName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", strings.Repeat("a", 1025), "a\rb", "a\nb", "a\x00b", ".", "..", "\xff", ".well-known/acme-challenge/x"} {
		var ie *InvalidError
		if err := validateName(bad); !errors.As(err, &ie) {
			t.Errorf("validateName(%q) = %v, want an InvalidError", bad, err)
		}
	}
	var ie *InvalidError
	if errors.As(validateName(""), &ie); ie.Reason != "required" {
		t.Errorf("an empty name: reason %q", ie.Reason)
	}
}

func TestSegments(t *testing.T) {
	for name, want := range map[string][]string{
		"a":            {"a"},
		"a/b c/d":      {"a", "b c", "d"},
		"a//b":         {"a", "", "b"},
		"/x":           {"", "x"},
		"a/":           {"a", ""},
		"a%2Fb":        {"a%2Fb"}, // a name is never decoded
		"photos/ü.png": {"photos", "ü.png"},
	} {
		if got := segments(name); !slices.Equal(got, want) {
			t.Errorf("segments(%q) = %q, want %q", name, got, want)
		}
	}
}

// PY-RR storedObjects: the default Content-Disposition production stores (ADR-022 §10.4).
func TestDefaultDisposition(t *testing.T) {
	for name, want := range map[string]string{
		"plain.txt":                "inline; filename*=utf-8''plain.txt",
		"dir/naïve 'q' 1+1%;x.txt": "inline; filename*=utf-8''na%C3%AFve%20%27q%27%201+1%25%3Bx.txt",
		"a/b/":                     "inline; filename*=utf-8''",
		"q\"x\\y,z(1)=*.txt":       "inline; filename*=utf-8''q%22x%5Cy%2Cz%281%29%3D%2A.txt",
		"!#$&+-.^_`|~AZaz09":       "inline; filename*=utf-8''!#$&+-.^_`|~AZaz09",
		"crlf?\u2028":              "inline; filename*=utf-8''crlf%3F%E2%80%A8",
	} {
		if got := defaultDisposition(name); got != want {
			t.Errorf("defaultDisposition(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestApplyDefaults(t *testing.T) {
	for _, c := range []struct {
		sf          Surface
		disposition string
	}{
		{SurfaceFirebase, "inline; filename*=utf-8''a.png"},
		{SurfaceConsole, "inline; filename*=utf-8''a.png"},
		{SurfaceGCS, ""}, // PY-RR gcs-json-api-seed: no disposition through GCS
	} {
		o := Object{Name: "p/a.png"}
		applyDefaults(&o, c.sf)
		if o.ContentType != "application/octet-stream" || o.ContentDisposition != c.disposition || o.ContentEncoding != "" {
			t.Errorf("%v: %+v", c.sf, o)
		}
	}
	o := Object{Name: "a.png", ContentType: "image/png", ContentDisposition: "attachment"}
	applyDefaults(&o, SurfaceFirebase)
	if o.ContentType != "image/png" || o.ContentDisposition != "attachment" {
		t.Errorf("a value set is replaced: %+v", o)
	}
}

func TestStripClientTokens(t *testing.T) {
	v := "planted"
	md := map[string]*string{TokensKey: &v, "k": &v}
	StripClientTokens(md)
	if _, ok := md[TokensKey]; ok || md["k"] == nil {
		t.Errorf("after StripClientTokens: %v", md)
	}
	md = map[string]*string{TokensKey: nil} // a null deletes no token either
	StripClientTokens(md)
	if len(md) != 0 {
		t.Error("a null token key is kept")
	}
	StripClientTokens(nil)
}

func TestDownloadTokens(t *testing.T) {
	a, b := newDownloadToken(), newDownloadToken()
	if a == b || len(a) != 36 || a[14] != '4' {
		t.Errorf("tokens %q, %q are not fresh version 4 UUIDs", a, b)
	}
	s := &Service{keys: testKeys(t)}
	sealed, err := s.sealTokens(482915736204, "a/b.png", []string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(a)) {
		t.Fatal("a sealed token reads in the clear")
	}
	got, err := s.openTokens(482915736204, "a/b.png", sealed)
	if err != nil || !slices.Equal(got, []string{a, b}) {
		t.Fatalf("openTokens = %v, %v", got, err)
	}
	// Bound to the project and the name: a copy to another row does not open.
	for _, c := range []struct {
		project int64
		name    string
	}{{482915736205, "a/b.png"}, {482915736204, "a/b.png2"}, {48291573620, "4|a/b.png"}} {
		if _, err := s.openTokens(c.project, c.name, sealed); err == nil {
			t.Errorf("tokens sealed for (482915736204, a/b.png) open for (%d, %s)", c.project, c.name)
		}
	}
	if sealed, err := s.sealTokens(1, "x", nil); sealed != nil || err != nil {
		t.Errorf("no tokens seal to %v, %v; want NULL", sealed, err)
	}
	if got, err := s.openTokens(1, "x", nil); got != nil || err != nil {
		t.Errorf("NULL opens to %v, %v", got, err)
	}
	for _, bad := range [][]string{{""}, {"a,b"}} {
		if _, err := s.sealTokens(1, "x", bad); err == nil {
			t.Errorf("sealTokens(%q) accepted", bad)
		}
	}
}

// A final session's refusal survives its row: every repeat answers with the same error.
func TestErrorsRoundTrip(t *testing.T) {
	for _, err := range []error{ErrPermissionDenied, ErrQuotaExceeded, ErrSizeMismatch, ErrChecksumMismatch,
		ErrUnauthenticated, ErrTooLarge, ErrPreconditionFailed, fmt.Errorf("finalize: %w", ErrPermissionDenied)} {
		code, msg := encodeError(err)
		if got := decodeError(code, msg); !errors.Is(err, got) {
			t.Errorf("%v round-trips to %v", err, got)
		}
	}
	code, msg := encodeError(&InvalidError{Reason: "invalid", Message: "Bad metadata."})
	var ie *InvalidError
	if got := decodeError(code, msg); !errors.As(got, &ie) || ie.Reason != "invalid" || ie.Message != "Bad metadata." {
		t.Errorf("an InvalidError round-trips to %v", got)
	}
	if code, _ := encodeError(errors.New("disk full")); decodeError(code, "") != ErrUnavailable {
		t.Error("an unnamed error is not kept as ErrUnavailable")
	}
	if decodeError("no_such_code", "") != ErrUnavailable {
		t.Error("an unknown code is not ErrUnavailable")
	}
	seen := map[string]bool{}
	for code, e := range namedErrors {
		if e.Code() != code || seen[code] || !strings.HasPrefix(e.Error(), "storage: ") {
			t.Errorf("named error %q", code)
		}
		seen[code] = true
	}
	if (&OffsetError{Received: 262144}).Error() != "storage: the chunk starts after the 262144 bytes received" {
		t.Error("OffsetError message")
	}
}

func TestLimits(t *testing.T) {
	if err := DefaultLimits().Validate(); err != nil {
		t.Fatalf("the defaults: %v", err)
	}
	unlimited := DefaultLimits()
	unlimited.ProjectBytes = 0
	unlimited.ObjectBytes = 1 << 40
	if err := unlimited.Validate(); err != nil {
		t.Errorf("an unlimited quota: %v", err)
	}
	for name, change := range map[string]func(*Limits){
		"object_bytes":                 func(l *Limits) { l.ObjectBytes = 0 },
		"project_bytes":                func(l *Limits) { l.ProjectBytes = -1 },
		"object_bytes must not exceed": func(l *Limits) { l.ObjectBytes = l.ProjectBytes + 1 },
		"must increase":                func(l *Limits) { l.EmptySessionTTL = l.UploadIdleTTL },
		"must increase ":               func(l *Limits) { l.UploadIdleTTL = l.UploadSessionTTL },
		"console_session_ttl":          func(l *Limits) { l.ConsoleSessionTTL = l.UploadSessionTTL + 1 },
		"anonymous_staged_fraction":    func(l *Limits) { l.AnonymousStagedFraction = 1.5 },
		"anonymous_staged_fraction ":   func(l *Limits) { l.AnonymousStagedFraction = -0.1 },
		"list_max_results":             func(l *Limits) { l.ListMaxResults = 1001 },
		"upload_requests_per_ip":       func(l *Limits) { l.UploadRequestsPerIP = 0 },
		"min_bytes_per_second":         func(l *Limits) { l.MinBytesPerSecond = 0 },
	} {
		l := DefaultLimits()
		change(&l)
		err := l.Validate()
		if err == nil || !strings.Contains(err.Error(), strings.TrimSpace(name)) {
			t.Errorf("%s: Validate = %v", name, err)
		}
	}
}

type fakeProjects struct {
	project tcore.Project
	err     error
}

func (f fakeProjects) ProjectForBucket(ctx context.Context, bucket string) (tcore.Project, error) {
	return f.project, f.err
}
func (f fakeProjects) GetProject(ctx context.Context, p *authn.Principal, ref string) (tcore.Project, error) {
	return f.project, f.err
}
func (f fakeProjects) CompiledRelease(ctx context.Context, project int64, release string) (*rules.Ruleset, error) {
	return nil, errors.New("no rules")
}
func (f fakeProjects) Audit(ctx context.Context, p *authn.Principal, project int64, action, target string, details map[string]any) error {
	return nil
}

func TestBucket(t *testing.T) {
	ctx := context.Background()
	active := tcore.Project{Number: 482915736204, State: "ACTIVE", StorageBucket: "chat-app.aruma.test"}
	s := &Service{projects: fakeProjects{project: active}}
	if b, err := s.Bucket(ctx, "chat-app.aruma.test"); err != nil || b != (Bucket{482915736204, "chat-app.aruma.test"}) {
		t.Errorf("Bucket = %+v, %v", b, err)
	}
	if _, err := s.Bucket(ctx, "other.aruma.test"); err != ErrBucketNotFound {
		t.Errorf("a name the project does not carry: %v", err)
	}
	s.projects = fakeProjects{err: apierr.New(apierr.NotFound, "Requested entity was not found.")}
	if _, err := s.Bucket(ctx, "gone.aruma.test"); err != ErrBucketNotFound {
		t.Errorf("an unknown bucket: %v", err)
	}
	s.projects = fakeProjects{err: errors.New("connection refused")}
	if _, err := s.Bucket(ctx, "chat-app.aruma.test"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("an outage: %v, want ErrUnavailable", err)
	}
}

func TestNew(t *testing.T) {
	ctx := context.Background()
	disk, _ := blob.NewDisk("/srv/aruma/storage")
	opts := Options{Limits: DefaultLimits(), Keys: testKeys(t), Projects: fakeProjects{}, Inflight: rules.NewInflight(1 << 20)}
	s, err := New(ctx, &pgxpool.Pool{}, disk, opts, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.documents.(rules.NoDocuments); !ok || s.gcGrace != time.Minute || s.now == nil || s.sessions.held == nil {
		t.Errorf("New's defaults: %+v", s)
	}
	if s.Limits() != DefaultLimits() {
		t.Error("Limits")
	}
	bad := opts
	bad.Limits.ObjectBytes = 0
	if _, err := New(ctx, &pgxpool.Pool{}, disk, bad, nil); err == nil {
		t.Error("invalid limits are accepted")
	}
	bad = opts
	bad.Inflight = nil
	if _, err := New(ctx, &pgxpool.Pool{}, disk, bad, nil); err == nil {
		t.Error("no Inflight is accepted")
	}
}

// Until S2-E, only admins pass, unless a test decides.
func TestAuthorizeBeforeTheRules(t *testing.T) {
	ctx := context.Background()
	s := &Service{}
	op := ruleOp{Method: rules.MethodGet, Name: "a.png", Surface: SurfaceFirebase}
	if err := s.authorize(ctx, Bucket{}, Principal{Kind: PrincipalAdmin}, op); err != nil {
		t.Errorf("an admin: %v", err)
	}
	for _, k := range []PrincipalKind{PrincipalAnonymous, PrincipalUser} {
		if err := s.authorize(ctx, Bucket{}, Principal{Kind: k}, op); err != ErrPermissionDenied {
			t.Errorf("kind %d: %v", k, err)
		}
	}
	s.authorizeHook = func(context.Context, Bucket, Principal, ruleOp) error { return nil }
	if err := s.authorize(ctx, Bucket{}, Principal{}, op); err != nil {
		t.Errorf("with a hook: %v", err)
	}
}

func TestNames(t *testing.T) {
	for v, want := range map[fmt.Stringer]string{SurfaceFirebase: "firebase", SurfaceGCS: "gcs", SurfaceConsole: "console", Surface(0): "invalid",
		UploadActive: "active", UploadFinal: "final", UploadCancelled: "cancelled", UploadStatus(0): "invalid"} {
		if v.String() != want {
			t.Errorf("%v, want %s", v, want)
		}
	}
}

func testKeys(t *testing.T) *crypto.Keys {
	t.Helper()
	k, err := crypto.NewKeys(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return k
}
