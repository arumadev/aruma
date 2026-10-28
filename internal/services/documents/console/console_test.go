package console

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/consolemount"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/documents/api"
	"github.com/arumadev/aruma/internal/services/documents/api/apitest"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// The test bed: the Document Database's service layer on a fresh database (apitest), with its
// control plane (ownership, audit log, rulesets), and the routes mounted the way the console API
// mounts them: authn.Authenticator.Require with the route's policy (ADR-010 S-4, S-5) and
// cross-origin protection, on httpx.Router. The owner of apitest's projects signs in as
// "session-owner"; badr owns a project of his own and none of the owner's.

var discard = slog.New(slog.DiscardHandler)

const (
	project = apitest.Project
	badrApp = "badr-app"
)

type credentials struct{ owner, badr *authn.Principal }

func (c credentials) VerifySession(_ context.Context, token string) (*authn.Principal, error) {
	switch token {
	case "session-owner":
		return c.owner, nil
	case "session-badr":
		return c.badr, nil
	}
	return nil, apierr.New(apierr.Unauthenticated, "Unknown session.")
}

func (c credentials) VerifyAccessToken(_ context.Context, token string) (*authn.Principal, error) {
	if token == authn.AccessTokenPrefix+"owner" {
		p := *c.owner
		p.Method = authn.MethodAccessToken
		return &p, nil
	}
	return nil, apierr.New(apierr.Unauthenticated, "Unknown token.")
}

func (credentials) VerifyServiceToken(context.Context, string) (*authn.Principal, error) {
	return nil, apierr.New(apierr.Unauthenticated, "Unknown token.")
}

type env struct {
	t    *testing.T
	docs *apitest.Env
	keys *crypto.Keys
	h    http.Handler
	// failAudit makes the audit log fail.
	failAudit bool
}

func newEnv(t *testing.T, enabled bool) *env {
	t.Helper()
	docs := apitest.New(t, apitest.Options{})
	badr := &authn.Principal{Kind: authn.KindAccount, Method: authn.MethodSession, AccountID: uuid.New()}
	if _, err := docs.Pool.Exec(t.Context(), `INSERT INTO control.accounts (id, email, password_hash) VALUES ($1, 'badr@example.com', 'x')`, badr.AccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := docs.Tenancy.CreateProject(t.Context(), badr, tcore.NewProject{ID: badrApp, DisplayName: badrApp}); err != nil {
		t.Fatal(err)
	}
	keys, err := crypto.NewKeys(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, docs: docs, keys: keys}
	auth := &authn.Authenticator{Verifier: credentials{owner: docs.Owner, badr: badr}, Keys: keys, CookieName: "aruma_session", Logger: discard}
	r := &httpx.Router{}
	m := &consolemount.Mount{
		Handle: func(p authn.Policy, method, pattern string, h http.HandlerFunc) {
			if p != anyCred {
				t.Errorf("%s %s: policy %+v, want a session or a CLI token", method, pattern, p)
			}
			r.Handle(method, pattern, auth.Require(p, h))
		},
		Project: func(r *http.Request) (consolemount.ProjectRef, error) {
			p, err := docs.Tenancy.GetProject(r.Context(), authn.FromContext(r.Context()), r.PathValue("project"))
			if err != nil {
				return consolemount.ProjectRef{}, err
			}
			return consolemount.ProjectRef{Number: p.Number, ID: p.ID}, nil
		},
		Fail: func(w http.ResponseWriter, r *http.Request, err error) { httpx.WriteError(w, r, discard, err) },
	}
	audit := func(ctx context.Context, p *authn.Principal, project int64, action, target string, details map[string]any) error {
		if e.failAudit {
			return io.ErrUnexpectedEOF
		}
		return docs.Tenancy.Audit(ctx, p, project, action, target, details)
	}
	var svc *api.Service
	if enabled {
		svc = docs.Service
	}
	Routes(svc, Options{Rules: docs.Tenancy, Audit: audit})(m)
	e.h = http.NewCrossOriginProtection().Handler(r)
	return e
}

type response struct {
	code int
	body []byte
	json map[string]any
}

func (r response) status() string {
	if e, ok := r.json["error"].(map[string]any); ok {
		s, _ := e["status"].(string)
		return s
	}
	return ""
}

func (r response) message() string {
	if e, ok := r.json["error"].(map[string]any); ok {
		s, _ := e["message"].(string)
		return s
	}
	return ""
}

// as is who sends a request: a session (with its CSRF token on writes, as the dashboard sends
// it), a CLI token, or nobody.
type as struct {
	session string
	csrf    bool
	token   string
}

var (
	owner       = as{session: "session-owner", csrf: true}
	ownerNoCSRF = as{session: "session-owner"}
	ownerCLI    = as{token: authn.AccessTokenPrefix + "owner"}
	badr        = as{session: "session-badr", csrf: true}
	nobody      = as{}
)

func (e *env) do(who as, method, target string, body any) response {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if who.session != "" {
		req.AddCookie(&http.Cookie{Name: "aruma_session", Value: who.session})
		if who.csrf {
			req.Header.Set(authn.CSRFHeader, e.keys.CSRFToken(who.session))
		}
	}
	if who.token != "" {
		req.Header.Set("Authorization", "Bearer "+who.token)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	out := response{code: rec.Code, body: rec.Body.Bytes()}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(out.body, &out.json)
	}
	return out
}

// ok sends a request that must answer 200.
func (e *env) ok(who as, method, target string, body any) response {
	e.t.Helper()
	res := e.do(who, method, target, body)
	if res.code != http.StatusOK {
		e.t.Fatalf("%s %s: %d %s", method, target, res.code, res.body)
	}
	return res
}

// path is a route under the project's documents, with its query.
func path(proj, rest string, query ...string) string {
	p := "/console/v1/projects/" + proj + "/documents" + rest
	if len(query) > 0 {
		v := url.Values{}
		for i := 0; i+1 < len(query); i += 2 {
			v.Set(query[i], query[i+1])
		}
		p += "?" + v.Encode()
	}
	return p
}

type auditRow struct {
	actor, action, target string
	details               map[string]any
}

// audits returns the project's audit log, oldest first.
func (e *env) audits() []auditRow {
	e.t.Helper()
	rows, err := e.docs.Tenancy.ListAudit(e.t.Context(), e.docs.Owner, project, tcore.ListOptions{PageSize: 100})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []auditRow
	for i := len(rows.Items) - 1; i >= 0; i-- {
		r := rows.Items[i]
		var details map[string]any
		_ = json.Unmarshal(r.Details, &details)
		actor := ""
		if r.ActorID != nil {
			actor = r.ActorID.String()
		}
		out = append(out, auditRow{actor: actor, action: r.Action, target: r.Target, details: details})
	}
	return out
}

func (e *env) lastAudit(action string) (auditRow, bool) {
	e.t.Helper()
	rows := e.audits()
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].action == action {
			return rows[i], true
		}
	}
	return auditRow{}, false
}

// every route of the package, with a body a write may take.
var routes = []struct {
	method, path string
	body         any
}{
	{"GET", "/usage", nil},
	{"GET", "/collections", nil},
	{"GET", "/documents?path=users", nil},
	{"POST", "/documents?path=users", map[string]any{}},
	{"POST", "/documents:recursiveDelete", map[string]any{"path": "users"}},
	{"GET", "/document?path=users/u1", nil},
	{"PATCH", "/document?path=users/u1", map[string]any{}},
	{"DELETE", "/document?path=users/u1", nil},
	{"GET", "/rules", nil},
	{"PUT", "/rules", map[string]any{"source": ""}},
	{"POST", "/rules:validate", map[string]any{"source": ""}},
	{"GET", "/rulesets", nil},
	{"GET", "/rulesets/00000000-0000-0000-0000-000000000000", nil},
	{"GET", "/indexes", nil},
	{"POST", "/indexes", map[string]any{"collectionGroup": "users"}},
	{"DELETE", "/indexes/users/abc", nil},
	{"GET", "/fields", nil},
	{"PUT", "/fields?group=users&field=a", map[string]any{}},
	{"DELETE", "/fields?group=users&field=a", nil},
	{"GET", "/ttl", nil},
	{"PUT", "/ttl?group=users&field=a", nil},
	{"DELETE", "/ttl?group=users&field=a", nil},
	{"GET", "/operations", nil},
}

// TestAuthorization: every route takes an account's credentials, a write the session's CSRF
// header, and the project must be the caller's, as on the Storage routes.
func TestAuthorization(t *testing.T) {
	e := newEnv(t, true)
	all := append([]struct {
		method, path string
		body         any
	}{{"GET", "", nil}}, routes...)
	for _, r := range all {
		if res := e.do(nobody, r.method, path(project, r.path), r.body); res.code != http.StatusUnauthorized {
			t.Errorf("%s %s without credentials: %d %s", r.method, r.path, res.code, res.body)
		}
		if res := e.do(badr, r.method, path(project, r.path), r.body); res.code != http.StatusNotFound {
			t.Errorf("%s %s of another's project: %d %s", r.method, r.path, res.code, res.body)
		}
		if r.method != "GET" {
			if res := e.do(ownerNoCSRF, r.method, path(project, r.path), r.body); res.code != http.StatusForbidden {
				t.Errorf("%s %s without the CSRF header: %d %s", r.method, r.path, res.code, res.body)
			}
		}
	}
	// A CLI token is an account's too.
	e.ok(ownerCLI, "GET", path(project, ""), nil)
	e.ok(ownerCLI, "POST", path(project, "/documents", "path", "cli"), map[string]any{"documentId": "x"})
	// Nothing reached the database from the refused writes.
	if res := e.ok(owner, "GET", path(project, "/collections"), nil); len(res.json["collectionIds"].([]any)) != 1 {
		t.Errorf("collections after the refused writes: %s", res.body)
	}
}

// TestDisabled: with the Document Database off, the info route says so and the others answer
// FAILED_PRECONDITION.
func TestDisabled(t *testing.T) {
	e := newEnv(t, false)
	res := e.ok(owner, "GET", path(project, ""), nil)
	if res.json["enabled"] != false || res.json["limits"] != nil {
		t.Errorf("info: %s", res.body)
	}
	for _, r := range routes {
		if res := e.do(owner, r.method, path(project, r.path), r.body); res.status() != "FAILED_PRECONDITION" {
			t.Errorf("%s %s: %d %s", r.method, r.path, res.code, res.body)
		}
	}
}

func TestInfoAndUsage(t *testing.T) {
	e := newEnv(t, true)
	res := e.ok(owner, "GET", path(project, ""), nil)
	lim, _ := res.json["limits"].(map[string]any)
	if res.json["enabled"] != true || res.json["databaseId"] != "(default)" ||
		res.json["name"] != "projects/"+project+"/databases/(default)" ||
		lim["documentBytes"] != float64(1<<20) || lim["fieldDepth"] != float64(20) || lim["pageSize"] != float64(defaultPage) {
		t.Errorf("info: %s", res.body)
	}
	// The usage of the owner's reads above (none: the info route reads no document), then of a
	// write and a read through the dashboard, which production counts too.
	e.ok(owner, "PATCH", path(project, "/document", "path", "users/u1"), map[string]any{"fields": map[string]any{}})
	e.ok(owner, "GET", path(project, "/document", "path", "users/u1"), nil)
	res = e.ok(owner, "GET", path(project, "/usage"), nil)
	totals, _ := res.json["totals"].(map[string]any)
	if res.json["period"] != "24h" || res.json["granularity"] != "hour" || res.json["available"] != nil ||
		totals["writes"] != float64(1) || totals["reads"] != float64(1) || len(res.json["hours"].([]any)) != 1 {
		t.Errorf("usage: %s", res.body)
	}
	for _, p := range []string{"60m", "24h", "7d", "30d"} {
		if res := e.ok(owner, "GET", path(project, "/usage", "period", p), nil); res.json["period"] != p {
			t.Errorf("usage over %s: %s", p, res.body)
		}
	}
	if res := e.do(owner, "GET", path(project, "/usage", "period", "1y"), nil); res.status() != "INVALID_ARGUMENT" {
		t.Errorf("usage over 1y: %d %s", res.code, res.body)
	}
}

// TestUsagePeriods: the usage route sums the hours of its period and keeps the greatest peak,
// from the stored hours as the flush leaves them, of the caller's project alone.
func TestUsagePeriods(t *testing.T) {
	e := newEnv(t, true)
	other, err := e.docs.Tenancy.GetProject(t.Context(), e.docs.Owner, apitest.OtherProject)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Hour)
	for _, r := range []struct {
		project int64
		ago     time.Duration
		reads   int
		peak    int
	}{
		{e.docs.Number, 0, 1, 2},
		{e.docs.Number, 3 * time.Hour, 10, 5},
		{e.docs.Number, 3 * 24 * time.Hour, 100, 1},
		{e.docs.Number, 20 * 24 * time.Hour, 1000, 9},
		{e.docs.Number, 40 * 24 * time.Hour, 10000, 0}, // past every period
		{other.Number, 0, 7, 7},
	} {
		if _, err := e.docs.Pool.Exec(t.Context(), `INSERT INTO documents.usage_hours (project_number, hour, reads, listeners_peak) VALUES ($1, $2, $3, $4)`,
			r.project, now.Add(-r.ago), r.reads, r.peak); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		period      string
		reads, peak float64
	}{{"60m", 1, 2}, {"24h", 11, 5}, {"7d", 111, 5}, {"30d", 1111, 9}} {
		res := e.ok(owner, "GET", path(project, "/usage", "period", c.period), nil)
		totals, _ := res.json["totals"].(map[string]any)
		if totals["reads"] != c.reads || totals["listenersPeak"] != c.peak {
			t.Errorf("%s: %s", c.period, res.body)
		}
	}
}

func TestBadPaths(t *testing.T) {
	e := newEnv(t, true)
	for _, c := range []struct{ method, path string }{
		{"GET", "/collections?path=users"},
		{"GET", "/documents?path=users/u1"},
		{"GET", "/documents?path="},
		{"GET", "/document?path=users"},
		{"GET", "/document?path=users//u1"},
		{"GET", "/documents?path=users&pageSize=0"},
	} {
		if res := e.do(owner, c.method, path(project, c.path), nil); res.status() != "INVALID_ARGUMENT" {
			t.Errorf("%s %s: %d %s", c.method, c.path, res.code, res.body)
		}
	}
}
