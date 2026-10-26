package server

import (
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/arumadev/aruma/internal/config"
	"github.com/arumadev/aruma/internal/platform/metrics"
	"github.com/arumadev/aruma/internal/services/documents/rest"
)

// Route ownership (ADR-027 §2.3): every service declares what it claims, no service's prefix is
// another's except an exception named here, and a corpus of requests, one at least per
// surface, reaches exactly the service expected. A new service is not mounted without its row.

// claim is what a service routes on.
type claim struct {
	service  string
	prefixes []string // path prefixes (ServeMux patterns ending in / included)
	exact    []string // exact paths
	rule     string   // a rule besides paths: host, content-type
}

// claims are the entry point's, as Handler mounts them.
func claims() []claim {
	return []claim{
		{service: "console", prefixes: []string{"/console/v1/"}},
		{service: "mgmt", prefixes: []string{"/v1beta1/", "/v1beta/", "/v1/projects/", "/v1alpha/projects/"}},
		{service: "oauth", prefixes: []string{"/oauth2/"}},
		{service: "auth", prefixes: AuthPrefixes},
		{service: "health", exact: []string{"/healthz", "/readyz"}},
		{service: "rtdb", exact: []string{"/.ws", "/.lp"}, rule: "a host under rtdb.domain; *.json with ?ns="},
		{service: "storage", prefixes: StoragePrefixes},
		{service: "gcs", prefixes: GCSPrefixes},
		{service: "documents", prefixes: []string{rest.Prefix + "*/databases"}, exact: rest.ChannelPaths,
			rule: "content-type application/grpc, application/grpc+…, application/grpc;…"},
		{service: "static", prefixes: []string{"/"}},
	}
}

// exceptions are the declared overlaps: the claim that wins, over the prefix it lies under.
var exceptions = map[string]string{
	// The Document Database's REST paths sit under the Management API's /v1/projects/ and are
	// routed first (ADR-027 §5.1).
	rest.Prefix + "*/databases": "/v1/projects/",
}

func TestRouteClaimsDoNotOverlap(t *testing.T) {
	type owned struct{ service, prefix string }
	var all []owned
	for _, c := range claims() {
		for _, p := range c.prefixes {
			all = append(all, owned{c.service, p})
		}
	}
	for _, a := range all {
		for _, b := range all {
			if a == b || a.service == b.service || b.prefix == "/" {
				continue
			}
			if strings.HasPrefix(a.prefix, b.prefix) || strings.HasPrefix(strings.ReplaceAll(a.prefix, "*", "p"), b.prefix) {
				if exceptions[a.prefix] != b.prefix {
					t.Errorf("%s's %s lies under %s's %s, and no exception names it", a.service, a.prefix, b.service, b.prefix)
				}
			}
		}
	}
	for p := range exceptions {
		if !slices.ContainsFunc(all, func(o owned) bool { return o.prefix == p }) {
			t.Errorf("exception %s names no claim", p)
		}
	}
}

// tagged answers with its service's name, so a test sees who served a request.
func tagged(service string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Served-By", service)
		w.WriteHeader(http.StatusOK)
	})
}

func TestRouteOwnership(t *testing.T) {
	cfg, err := config.Load("", func(k string) string {
		return map[string]string{
			"ARUMA_DATABASE_URL": "postgres://unused",
			"ARUMA_PUBLIC_URL":   "https://aruma.test",
			"ARUMA_MASTER_KEY":   "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
			"ARUMA_RTDB_DOMAIN":  "rtdb.aruma.test",
		}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	h := Handlers{
		Console: tagged("console"), Mgmt: tagged("mgmt"), OAuth: tagged("oauth"), Auth: tagged("auth"),
		Static: tagged("static"), RTDB: tagged("rtdb"), Storage: tagged("storage"), GCS: tagged("gcs"),
		Documents: tagged("documents"), DocumentsREST: tagged("documents"), DocumentsOwns: rest.Owns,
	}
	handler := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, metrics.New("test"), h).Handler()
	declared := map[string]bool{"health": true}
	for _, c := range claims() {
		declared[c.service] = true
	}
	served := map[string]bool{"health": true}
	for _, c := range []struct {
		method, host, path, contentType, want string
	}{
		{"POST", "", "/google.firestore.v1.Firestore/Listen", "application/grpc", "documents"},
		{"POST", "", "/google.firestore.v1.Firestore/GetDocument", "application/grpc+proto", "documents"},
		{"POST", "", "/google.firestore.admin.v1.FirestoreAdmin/GetDatabase", "application/grpc", "documents"},
		{"POST", "", "/v0/b/x/o", "application/grpc", "documents"},
		{"POST", "", "/google.firestore.v1.Firestore/Listen/channel", "application/x-www-form-urlencoded", "documents"},
		{"GET", "", "/v1/projects/p/databases/(default)/documents/c/d", "", "documents"},
		{"POST", "", "/v1/projects/p/databases/(default)/documents:batchGet", "application/json", "documents"},
		{"GET", "", "/v1/projects/p/databases/(default)", "", "documents"},
		{"GET", "", "/v1/projects/p/databases", "", "documents"},
		{"GET", "", "/v1/projects/p/rulesets", "", "mgmt"},
		{"GET", "", "/v1/projects/p:getDefaultBucket", "", "mgmt"},
		{"GET", "", "/v1beta1/projects", "", "mgmt"},
		{"GET", "", "/console/v1/me", "", "console"},
		{"POST", "", "/oauth2/v1/token", "", "oauth"},
		{"POST", "", "/identitytoolkit.googleapis.com/v1/accounts:signUp", "", "auth"},
		{"GET", "", "/.ws", "", "rtdb"},
		{"GET", "chat-default-rtdb.rtdb.aruma.test", "/users.json", "", "rtdb"},
		{"GET", "", "/v0/b/bucket/o/a.png", "", "storage"},
		{"POST", "", "/upload/storage/v1/b/bucket/o", "", "gcs"},
		{"GET", "", "/", "", "static"},
		{"GET", "", "/projects", "", "static"},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		if c.host != "" {
			r.Host = c.host
		}
		if c.contentType != "" {
			r.Header.Set("Content-Type", c.contentType)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, r)
		if got := rec.Header().Get("X-Served-By"); got != c.want {
			t.Errorf("%s %s%s (%s): served by %q, want %q", c.method, c.host, c.path, c.contentType, got, c.want)
		}
		served[c.want] = true
	}
	for _, s := range slices.Sorted(maps.Keys(declared)) {
		if !served[s] {
			t.Errorf("no request of the corpus reaches %s", s)
		}
	}
}

// Disabled, the Document Database still answers gRPC, in gRPC form (ADR-027 §2.1), and its REST
// paths fall through to the Management API.
func TestDocumentsDisabled(t *testing.T) {
	ts := newServer(t, nil, Handlers{Mgmt: tagged("mgmt")})
	req, _ := http.NewRequest("POST", ts.URL+"/google.firestore.v1.Firestore/GetDocument", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/grpc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Grpc-Status") != "12" ||
		resp.Header.Get("Grpc-Message") != "The Document Database is not enabled on this server." {
		t.Errorf("gRPC while disabled: %d %v", resp.StatusCode, resp.Header)
	}
	if got := get(t, ts, "GET", "/v1/projects/p/databases/(default)/documents/c/d", nil).Header.Get("X-Served-By"); got != "mgmt" {
		t.Errorf("REST while disabled: served by %q", got)
	}
}

// The data paths are open to browsers; the Admin API's paths stay closed (ADR-027 §5.2). The
// surface's own CORS answers are its tests'; here, the entry point keeps them.
func TestDocumentsCORS(t *testing.T) {
	ts := newServer(t, nil, Handlers{DocumentsREST: rest.New(rest.Options{}), DocumentsOwns: rest.Owns})
	pre := get(t, ts, "OPTIONS", "/v1/projects/p/databases/(default)/documents/c/d", map[string]string{
		"Origin": "https://app.example", "Access-Control-Request-Method": "PATCH",
	})
	if pre.StatusCode != http.StatusNoContent || pre.Header.Get("Access-Control-Allow-Origin") != "*" ||
		pre.Header.Get("Cross-Origin-Resource-Policy") != "cross-origin" {
		t.Errorf("data path preflight: %d %v", pre.StatusCode, pre.Header)
	}
	for _, path := range []string{"/v1/projects/p/databases/(default)", "/v1/projects/p/databases/(default)/collectionGroups/c/indexes"} {
		resp := get(t, ts, "GET", path, map[string]string{"Origin": "https://evil.example"})
		h := resp.Header
		if h.Get("Access-Control-Allow-Origin") != "" || h.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: %v", path, h)
		}
	}
}
