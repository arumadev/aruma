package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/services/storage/queries"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// The tests of the contract commit (S2-0): the exported API is pinned, and the parts that work
// from the contract (helpers, errors, limits, Bucket, New, authorize) are tested. Each task tests
// its own behaviour in its own files.

// The signatures of the contract: a change does not compile.
var (
	_ func(context.Context, *pgxpool.Pool, blob.Store, Options, *slog.Logger) (*Service, error)                                              = New
	_ func(*Service) Limits                                                                                                                  = (*Service).Limits
	_ func(*Service) time.Time                                                                                                               = (*Service).Now
	_ func(*Service, context.Context, string) (Bucket, error)                                                                                = (*Service).Bucket
	_ func(*Service, context.Context, Bucket, string, Principal, Surface, int64) (Object, error)                                             = (*Service).Stat
	_ func(*Service, context.Context, Bucket, string, Principal, string, int64) (Object, blob.Reader, error)                                 = (*Service).Open
	_ func(*Service, context.Context, Bucket, Principal, ListOptions) (ListPage, error)                                                      = (*Service).List
	_ func(*Service, context.Context, Bucket, string, Principal, Surface, Patch, Preconditions) (Object, error)                              = (*Service).Update
	_ func(*Service, context.Context, Bucket, string, Principal, Surface, Preconditions) error                                               = (*Service).Delete
	_ func(Surface, Preconditions) error                                                                                                     = checkPre
	_ func(*Service, context.Context, Bucket, string, Bucket, string, Principal, *Patch, Preconditions, ...SourceConditions) (Object, error) = (*Service).Rewrite
	_ func(*Service, context.Context, Bucket, string, Principal) (Object, error)                                                             = (*Service).CreateToken
	_ func(*Service, context.Context, Bucket, string, Principal, string) (Object, error)                                                     = (*Service).DeleteToken
	_ func(*Service, context.Context, Bucket) ([]CORSRule, error)                                                                            = (*Service).CORS
	_ func(*Service, context.Context, Bucket, Principal, []CORSRule, ...BucketConditions) (BucketInfo, error)                                = (*Service).SetCORS
	_ func(*Service, context.Context, Bucket) (BucketInfo, error)                                                                            = (*Service).BucketInfo
	_ func(*Service) error                                                                                                                   = (*Service).Close
	_ func(*Service, netip.Addr) (bool, time.Duration)                                                                                       = (*Service).AllowUploadStart
	_ func(*Service) *Metrics                                                                                                                = (*Service).Metrics
	_ func() *Metrics                                                                                                                        = NewMetrics
	_ func(Limits) int64                                                                                                                     = Limits.BodyCap
	_ func(*Service, context.Context) (pgx.Tx, error)                                                                                        = (*Service).begin
	_ func(*Service, context.Context, func(*queries.Queries) error) error                                                                    = (*Service).inTx
	_ func(*Service, context.Context) error                                                                                                  = (*Service).warnUnsafeDurability
	_ func(context.Context, *queries.Queries, int64, string, int64) error                                                                    = releaseObject
	_ func(error) (*Error, bool)                                                                                                             = finalError
	_ func(error) (string, string, bool)                                                                                                     = encodeError
	_ func(string) error                                                                                                                     = decodeError
	_ func(*Service, int64, string, []byte, []string) ([]byte, error)                                                                        = (*Service).sealSessionTokens
	_ func(*Service, int64, string, []byte, []byte) ([]string, error)                                                                        = (*Service).openSessionTokens
	_ func(Patch) (json.RawMessage, error)                                                                                                   = encodeAttrs
	_ func(json.RawMessage) (Patch, error)                                                                                                   = decodeAttrs
	_ func(Object) (json.RawMessage, error)                                                                                                  = encodeSnapshot
	_ func(json.RawMessage) (Object, error)                                                                                                  = decodeSnapshot
	_ func(*Service, []CORSRule, string, string) (bool, CORSRule)                                                                            = (*Service).MatchCORS
	_ func(*Service, context.Context, Bucket) (Usage, error)                                                                                 = (*Service).Usage
	_ func(*Service, context.Context, Bucket, Principal, UploadSpec, io.Reader, int64) (Object, error)                                       = (*Service).Insert
	_ func(*Service, context.Context, Bucket, Principal, UploadSpec) (string, error)                                                         = (*Service).StartUpload
	_ func(*Service, context.Context, Bucket, string, string, *Principal, Chunk) (UploadState, error)                                        = (*Service).WriteUpload
	_ func(*Service, context.Context, Bucket, string, string) (UploadState, error)                                                           = (*Service).QueryUpload
	_ func(*Service, context.Context, Bucket, string, string) (UploadState, error)                                                           = (*Service).CancelUpload
	_ func(*Service, context.Context, int64) error                                                                                           = (*Service).PurgeProject
	_ func(*Service, context.Context) error                                                                                                  = (*Service).Boot
	_ func(*Service, context.Context) error                                                                                                  = (*Service).Run
	_ func(*Service, context.Context, objectWrite) (Object, error)                                                                           = (*Service).commitObject
	_ func(*Service, context.Context, Bucket, Principal, ruleOp) error                                                                       = (*Service).authorize
	_ func(string) error                                                                                                                     = validateName
	_ func(string) []string                                                                                                                  = segments
	_ func(*Object, Surface)                                                                                                                 = applyDefaults
	_ func(string) string                                                                                                                    = defaultDisposition
	_ func(map[string]*string)                                                                                                               = StripClientTokens
	_ func() string                                                                                                                          = newDownloadToken
	_ func(*Service, int64, string, []string) ([]byte, error)                                                                                = (*Service).sealTokens
	_ func(*Service, int64, string, []byte) ([]string, error)                                                                                = (*Service).openTokens
)

// No task adds an exported method to Service: the surfaces are written against this list.
func TestServiceMethodsArePinned(t *testing.T) {
	want := []string{"AllowUploadStart", "Boot", "Bucket", "BucketInfo", "CORS", "CancelUpload", "Close", "CreateToken",
		"Delete", "DeleteToken", "Insert", "Limits", "List", "MatchCORS", "Metrics", "Now", "Open", "PurgeProject",
		"QueryUpload", "Rewrite", "Run", "SetCORS", "StartUpload", "Stat", "Update", "Usage", "WriteUpload"}
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
		{Chunk{}, "Offset int64; Body io.Reader; Length int64; Finalize bool; Total int64; Want core.Checksums"},
		{BucketInfo{}, "Bucket core.Bucket; Metageneration int64; Created time.Time; Updated time.Time; CORS []core.CORSRule"},
		{Options{}, "Limits core.Limits; Keys *crypto.Keys; Projects core.Projects; Documents rules.Documents; Inflight *rules.Inflight; " +
			"Users idtoken.Users; DataDir string; GCGrace time.Duration; Metrics *core.Metrics; Now func() time.Time"},
		{Limits{}, "ObjectBytes int64; ProjectBytes int64; MinFreeBytes int64; EmptySessionTTL time.Duration; UploadIdleTTL time.Duration; " +
			"UploadSessionTTL time.Duration; ConsoleSessionTTL time.Duration; AnonymousStagedFraction float64; MetadataBytes int; " +
			"JSONBodyBytes int64; HeaderFieldBytes int; UploadSessionsPerProject int; UploadSessionsPerUser int; UploadSessionsPerIP int; " +
			"UploadStartsPerMinutePerIP int; UploadStartsPerSecond int; UploadRequestsPerIP int; MinBytesPerSecond int64; ListMaxResults int"},
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

// Projects is the control plane's side of the contract: tenancy implements exactly these.
func TestProjectsIsPinned(t *testing.T) {
	typ := reflect.TypeFor[Projects]()
	var got []string
	for i := range typ.NumMethod() {
		m := typ.Method(i)
		got = append(got, m.Name+" "+m.Type.String())
	}
	want := []string{
		"Audit func(context.Context, *authn.Principal, int64, string, string, map[string]interface {}) error",
		"CompiledRelease func(context.Context, int64, string) (*rules.Ruleset, error)",
		"GetProject func(context.Context, *authn.Principal, string) (core.Project, error)",
		"ProjectForBucket func(context.Context, string) (core.Project, error)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Projects:\n got %q\nwant %q", got, want)
	}
	var _ Projects = (*tcore.Service)(nil)
}

// The named errors, by code: a new one goes through the integrator.
func TestErrorsArePinned(t *testing.T) {
	want := []string{"bucket_not_found", "checksum_mismatch", "invalid_argument", "invalid_page_token", "not_found", "overloaded",
		"permission_denied", "precondition_failed", "quota_exceeded", "size_mismatch", "too_large", "too_many_uploads",
		"unauthenticated", "unavailable", "upload_cancelled", "upload_final", "upload_not_found"}
	got := slices.Sorted(maps.Keys(namedErrors))
	if !slices.Equal(got, want) {
		t.Errorf("named errors %q, want %q", got, want)
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

// Only the terminal refusals of ADR-022 §6.1 make a session final, and each survives its row:
// every repeat answers with the same error, so the same status.
func TestFinalErrors(t *testing.T) {
	for _, err := range []error{ErrPermissionDenied, ErrQuotaExceeded, ErrSizeMismatch, ErrChecksumMismatch,
		ErrTooLarge, ErrUnauthenticated, ErrUploadCancelled, fmt.Errorf("finalize: %w", ErrPermissionDenied)} {
		code, msg, ok := encodeError(err)
		if !ok || msg == "" || !errors.Is(err, decodeError(code)) {
			t.Errorf("%v: encodes to %q, %v; decodes to %v", err, code, ok, decodeError(code))
		}
	}
	for _, err := range []error{ErrOverloaded, ErrUnavailable, ErrNotFound, ErrUploadNotFound, ErrBucketNotFound,
		ErrPreconditionFailed, ErrUploadFinal, ErrTooManyUploads, ErrInvalidPageToken, ErrInvalidArgument, &OffsetError{Received: 1},
		&InvalidError{Reason: "invalid", Message: "x"}, errors.New("disk full"), nil} {
		if _, ok := finalError(err); ok {
			t.Errorf("%v makes a session final", err)
		}
		if _, _, ok := encodeError(err); ok {
			t.Errorf("%v is encoded as a final refusal", err)
		}
	}
	for _, code := range []string{"no_such_code", "overloaded", "not_found", ""} {
		if decodeError(code) != ErrUnavailable {
			t.Errorf("decodeError(%q) is not ErrUnavailable", code)
		}
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

func TestSessionTokens(t *testing.T) {
	s := &Service{keys: testKeys(t)}
	id := bytes.Repeat([]byte{9}, 32)
	tokens := []string{"plaintext-token-one", "plaintext-token-two"}
	sealed, err := s.sealSessionTokens(482915736204, "a.png", id, tokens)
	if err != nil || bytes.Contains(sealed, []byte(tokens[0])) {
		t.Fatalf("sealSessionTokens = %x, %v", sealed, err)
	}
	if got, err := s.openSessionTokens(482915736204, "a.png", id, sealed); err != nil || !slices.Equal(got, tokens) {
		t.Fatalf("openSessionTokens = %v, %v", got, err)
	}
	if _, err := s.openSessionTokens(482915736204, "a.png", bytes.Repeat([]byte{8}, 32), sealed); err == nil {
		t.Error("a session's tokens open for another session")
	}
	if _, err := s.openTokens(482915736204, "a.png", sealed); err == nil {
		t.Error("a session's sealed tokens open as an object's")
	}
	obj, _ := s.sealTokens(482915736204, "a.png", []string{"t1"})
	if _, err := s.openSessionTokens(482915736204, "a.png", id, obj); err == nil {
		t.Error("an object's sealed tokens open as a session's")
	}
}

func TestAttrs(t *testing.T) {
	ct, v := "image/png", "1"
	raw, err := encodeAttrs(Patch{Fields: map[string]*string{FieldContentType: &ct}, Metadata: map[string]*string{"k": &v, "gone": nil},
		ClearMetadata: true, Tokens: &[]string{"secret-token"}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("secret-token")) || bytes.Contains(raw, []byte(TokensKey)) {
		t.Errorf("attrs carry tokens: %s", raw)
	}
	p, err := decodeAttrs(raw)
	if err != nil || *p.Fields[FieldContentType] != ct || *p.Metadata["k"] != "1" || p.Metadata["gone"] != nil ||
		!p.ClearMetadata || p.Tokens != nil {
		t.Errorf("decodeAttrs = %+v, %v", p, err)
	}
	if _, ok := p.Metadata["gone"]; !ok {
		t.Error("a key to delete is lost")
	}
	if _, err := encodeAttrs(Patch{Metadata: map[string]*string{TokensKey: &v}}); err == nil {
		t.Error("the token key is accepted in attrs")
	}
}

func TestSnapshot(t *testing.T) {
	at := time.Date(2026, 10, 24, 12, 0, 0, 123456000, time.UTC)
	o := Object{Bucket: "b", Name: "a/b.png", Generation: 1784647629568292, Metageneration: 2, Size: 13, MD5: [16]byte{1, 2},
		CRC32C: 0xdeadbeef, ContentType: "image/png", ContentDisposition: "inline", Metadata: map[string]string{"k": "v"},
		DownloadTokens: []string{"secret-token"}, Created: at, Updated: at}
	raw, err := encodeSnapshot(o)
	if err != nil || bytes.Contains(raw, []byte("secret-token")) {
		t.Fatalf("encodeSnapshot = %s, %v", raw, err)
	}
	got, err := decodeSnapshot(raw)
	o.DownloadTokens = nil
	if err != nil || !reflect.DeepEqual(got, o) {
		t.Errorf("decodeSnapshot = %+v, %v; want %+v", got, err, o)
	}
}

func TestAllowUploadStart(t *testing.T) {
	lim := DefaultLimits()
	lim.UploadStartsPerMinutePerIP, lim.UploadStartsPerSecond = 2, 3
	s, err := New(context.Background(), &pgxpool.Pool{}, &blob.Disk{}, Options{Limits: lim, Keys: testKeys(t),
		Projects: fakeProjects{}, Inflight: rules.NewInflight(1 << 20), DataDir: "/srv/aruma/storage"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, b := netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("2001:db8::1")
	// a's third start is refused for its address; b's /64 is one address; the process allows
	// three starts that passed their address's limit, then refuses.
	for i, c := range []struct {
		ip netip.Addr
		ok bool
	}{{a, true}, {a, true}, {a, false}, {b, true}, {netip.MustParseAddr("2001:db8::2"), false}, {netip.Addr{}, false}} {
		if ok, wait := s.AllowUploadStart(c.ip); ok != c.ok || (!ok && wait <= 0) {
			t.Errorf("start %d from %v: %v, %v; want %v", i, c.ip, ok, wait, c.ok)
		}
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
		"5 TiB":                        func(l *Limits) { l.ProjectBytes, l.ObjectBytes = 0, MaxObjectBytes+1 },
		"json_body_bytes must not":     func(l *Limits) { l.JSONBodyBytes = MaxJSONBodyBytes + 1 },
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
	opts := Options{Limits: DefaultLimits(), Keys: testKeys(t), Projects: fakeProjects{}, Inflight: rules.NewInflight(1 << 20),
		DataDir: "/srv/aruma/storage"}
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
	bad.DataDir = "storage"
	if _, err := New(ctx, &pgxpool.Pool{}, disk, bad, nil); err == nil {
		t.Error("a relative data directory is accepted")
	}
	if l := DefaultLimits(); l.BodyCap() != 5<<30+64<<10+64<<10 {
		t.Errorf("BodyCap = %d", l.BodyCap())
	}
	bad = opts
	bad.Inflight = nil
	if _, err := New(ctx, &pgxpool.Pool{}, disk, bad, nil); err == nil {
		t.Error("no Inflight is accepted")
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

// Preconditions are GCS's alone: from any other surface, every write refuses them before it does
// anything (a 412 would keep a /v0 session active and loop the mobile SDKs).
func TestPreconditionsOnlyOnGCS(t *testing.T) {
	ctx := context.Background()
	s := &Service{}
	zero := int64(0)
	pre := Preconditions{IfGenerationMatch: &zero}
	for _, sf := range []Surface{SurfaceFirebase, SurfaceConsole, 0} {
		spec := UploadSpec{Name: "a.png", Surface: sf, Pre: pre, Declared: -1}
		if _, err := s.StartUpload(ctx, Bucket{}, Principal{}, spec); err != ErrInvalidArgument {
			t.Errorf("%v StartUpload: %v", sf, err)
		}
		if _, err := s.Insert(ctx, Bucket{}, Principal{}, spec, nil, 0); err != ErrInvalidArgument {
			t.Errorf("%v Insert: %v", sf, err)
		}
		if _, err := s.Update(ctx, Bucket{}, "a.png", Principal{}, sf, Patch{}, pre); err != ErrInvalidArgument {
			t.Errorf("%v Update: %v", sf, err)
		}
		if err := s.Delete(ctx, Bucket{}, "a.png", Principal{}, sf, pre); err != ErrInvalidArgument {
			t.Errorf("%v Delete: %v", sf, err)
		}
	}
	if err := checkPre(SurfaceGCS, pre); err != nil {
		t.Errorf("GCS: %v", err)
	}
	for _, sf := range []Surface{SurfaceFirebase, SurfaceConsole, SurfaceGCS} {
		if err := checkPre(sf, Preconditions{}); err != nil {
			t.Errorf("%v without preconditions: %v", sf, err)
		}
	}
}
