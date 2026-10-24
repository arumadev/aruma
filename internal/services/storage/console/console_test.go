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
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/consolemount"
	"github.com/arumadev/aruma/internal/platform/crypto"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/rules"
	"github.com/arumadev/aruma/internal/services/storage/blob"
	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/store/storetest"
	tcore "github.com/arumadev/aruma/internal/tenancy/core"
)

// The test bed: a Storage service on a fresh database and a data directory of its own, the real
// control plane (ownership, buckets, audit log, rulesets), and the routes mounted the way the
// console API mounts them: authn.Authenticator.Require with the route's policy (ADR-010 S-4, S-5)
// and cross-origin protection, on httpx.Router. The body cap of the entry point is left out on
// purpose, so the routes' own cap is what the tests see.

const (
	publicURL = "https://aruma.test"
	domain    = "aruma.test"
	amal      = "00000000-0000-7000-8000-000000000001" // owns chat-app
	badr      = "00000000-0000-7000-8000-000000000002" // owns other-app
	chatApp   = 482915736204
)

var discard = slog.New(slog.DiscardHandler)

// credentials stands in for the account verifier: a session token and a CLI token per account.
type credentials struct{}

func principalOf(account string, m authn.Method) *authn.Principal {
	return &authn.Principal{Kind: authn.KindAccount, Method: m, AccountID: uuid.MustParse(account)}
}

func (credentials) VerifySession(_ context.Context, token string) (*authn.Principal, error) {
	switch token {
	case "session-amal":
		return principalOf(amal, authn.MethodSession), nil
	case "session-badr":
		return principalOf(badr, authn.MethodSession), nil
	}
	return nil, apierr.New(apierr.Unauthenticated, "Unknown session.")
}

func (credentials) VerifyAccessToken(_ context.Context, token string) (*authn.Principal, error) {
	if token == authn.AccessTokenPrefix+"amal" {
		return principalOf(amal, authn.MethodAccessToken), nil
	}
	return nil, apierr.New(apierr.Unauthenticated, "Unknown token.")
}

func (credentials) VerifyServiceToken(context.Context, string) (*authn.Principal, error) {
	return nil, apierr.New(apierr.Unauthenticated, "Unknown token.")
}

// rulesStub is tenancy's rulesets and releases, with GetRuleset kept in memory until S5-A
// writes tenancy's (it panics on main): the rest is tenancy's own.
type rulesStub struct {
	*tcore.Service
	mu    sync.Mutex
	saved map[string]tcore.Ruleset
}

func (s *rulesStub) CreateRuleset(ctx context.Context, p *authn.Principal, project string, svc rules.Service, files []rules.File) (tcore.Ruleset, rules.Issues, error) {
	rs, issues, err := s.Service.CreateRuleset(ctx, p, project, svc, files)
	if err == nil {
		s.mu.Lock()
		s.saved[rs.Name] = rs
		s.mu.Unlock()
	}
	return rs, issues, err
}

func (s *rulesStub) GetRuleset(_ context.Context, _ *authn.Principal, _, name string) (tcore.Ruleset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs, ok := s.saved[name]
	if !ok {
		return tcore.Ruleset{}, apierr.New(apierr.NotFound, "Requested entity was not found.")
	}
	return rs, nil
}

type route struct{ method, pattern string }

type env struct {
	t       *testing.T
	svc     *core.Service
	tenancy *tcore.Service
	keys    *crypto.Keys
	h       http.Handler
	routes  []route
	bucket  core.Bucket
	pool    *pgxpool.Pool
	// audit wraps tenancy's audit log; failAudit makes it fail.
	failAudit bool
}

func newEnv(t *testing.T, enabled bool, tune ...func(*core.Limits)) *env {
	t.Helper()
	pool := storetest.NewPool(t)
	ctx := t.Context()
	for _, sql := range []string{
		`INSERT INTO control.accounts (id, email, password_hash) VALUES
		 ('` + amal + `', 'amal@example.com', 'x'), ('` + badr + `', 'badr@example.com', 'x')`,
		`INSERT INTO control.projects (project_number, project_id, display_name, owner_account_id, etag, auth_domain)
		 VALUES (482915736204, 'chat-app', 'Chat', '` + amal + `', '1_a', 'aruma.test'),
		        (582915736204, 'other-app', 'Other', '` + badr + `', '1_b', 'aruma.test')`,
	} {
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := crypto.NewKeys(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ten := tcore.New(pool, keys, nil, tcore.Options{PublicURL: publicURL, ProjectDomain: domain,
		RulesLimits: rules.DefaultLimits()}, discard)
	e := &env{t: t, tenancy: ten, keys: keys, pool: pool, bucket: core.Bucket{Project: chatApp, Name: "chat-app." + domain}}
	if enabled {
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
		svc, err := core.New(ctx, pool, disk, core.Options{Limits: lim, Keys: keys, Projects: ten,
			Inflight: rules.NewInflight(1 << 20), DataDir: dir}, discard)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Boot(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = svc.Close() })
		e.svc = svc
	}
	auth := &authn.Authenticator{Verifier: credentials{}, Keys: keys, CookieName: "aruma_session", Logger: discard}
	r := &httpx.Router{}
	m := &consolemount.Mount{
		Handle: func(p authn.Policy, method, pattern string, h http.HandlerFunc) {
			if p != anyCred {
				t.Errorf("%s %s: policy %+v, want a session or a CLI token", method, pattern, p)
			}
			e.routes = append(e.routes, route{method, pattern})
			r.Handle(method, pattern, auth.Require(p, h))
		},
		Project: func(r *http.Request) (consolemount.ProjectRef, error) {
			p, err := ten.GetProject(r.Context(), authn.FromContext(r.Context()), r.PathValue("project"))
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
		return ten.Audit(ctx, p, project, action, target, details)
	}
	var svc *core.Service
	if enabled {
		svc = e.svc
	}
	Routes(svc, Options{PublicURL: publicURL, ProjectDomain: domain, Audit: audit,
		Rules: &rulesStub{Service: ten, saved: map[string]tcore.Ruleset{}}})(m)
	e.h = http.NewCrossOriginProtection().Handler(r)
	return e
}

type response struct {
	code   int
	header http.Header
	body   []byte
	json   map[string]any
}

func (r response) status() string {
	if e, ok := r.json["error"].(map[string]any); ok {
		s, _ := e["status"].(string)
		return s
	}
	return ""
}

// as is who sends a request: a session (with its CSRF token on writes, as the dashboard
// sends it), a CLI token, or nobody.
type as struct {
	session string
	csrf    bool
	token   string
}

var (
	amalSession = as{session: "session-amal", csrf: true}
	amalCLI     = as{token: authn.AccessTokenPrefix + "amal"}
	badrSession = as{session: "session-badr", csrf: true}
	nobody      = as{}
)

func (e *env) do(who as, method, target string, body any) response {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
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
	e.authorize(req, who)
	return e.serve(req)
}

func (e *env) authorize(req *http.Request, who as) {
	if who.session != "" {
		req.AddCookie(&http.Cookie{Name: "aruma_session", Value: who.session})
		if who.csrf {
			req.Header.Set(authn.CSRFHeader, e.keys.CSRFToken(who.session))
		}
	}
	if who.token != "" {
		req.Header.Set("Authorization", "Bearer "+who.token)
	}
}

func (e *env) serve(req *http.Request) response {
	e.t.Helper()
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	out := response{code: rec.Code, header: rec.Header(), body: rec.Body.Bytes()}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(out.body, &out.json)
	}
	return out
}

// path is a route under chat-app's storage, with its query.
func path(rest string, query ...string) string {
	p := "/console/v1/projects/chat-app/storage" + rest
	if len(query) > 0 {
		v := url.Values{}
		for i := 0; i+1 < len(query); i += 2 {
			v.Set(query[i], query[i+1])
		}
		p += "?" + v.Encode()
	}
	return p
}

// insert stores an object as an admin through GCS, as an Admin SDK would.
func (e *env) insert(name, contentType, body string) core.Object {
	e.t.Helper()
	spec := core.UploadSpec{Name: name, Surface: core.SurfaceGCS, Declared: -1}
	if contentType != "" {
		spec.Attrs.Fields = map[string]*string{core.FieldContentType: new(contentType)}
	}
	o, err := e.svc.Insert(e.t.Context(), e.bucket, core.Principal{Kind: core.PrincipalAdmin, ServiceAccount: "sa"},
		spec, strings.NewReader(body), int64(len(body)))
	if err != nil {
		e.t.Fatalf("insert %s: %v", name, err)
	}
	return o
}

type auditRow struct {
	actor, action, target string
	details               map[string]any
}

// audits returns chat-app's audit log, oldest first.
func (e *env) audits() []auditRow {
	e.t.Helper()
	rows, err := e.tenancy.ListAudit(e.t.Context(), principalOf(amal, authn.MethodSession), "chat-app", tcore.ListOptions{PageSize: 100})
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
