// Package console is the private JSON API behind the dashboard, mounted at /console/v1/.
//
// Callers authenticate with the session cookie (and the CSRF header on writes) or with a
// personal CLI token. Account security operations (password, sessions, tokens) accept the
// session only, so a leaked CLI token cannot take over the account.
package console

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/platform/ratelimit"
	"github.com/osama300/aurma-project/internal/tenancy/core"
)

// Options configure the console API.
type Options struct {
	Version       string
	SecureCookies bool
}

// API is the console API.
type API struct {
	routes  []Route
	svc     *core.Service
	auth    *authn.Authenticator
	log     *slog.Logger
	opts    Options
	cookie  string
	ipLimit *ratelimit.Limiter
	backoff *ratelimit.Backoff
}

// CookieName returns the session cookie name: the __Host- prefix whenever cookies are Secure.
func CookieName(secure bool) string {
	if secure {
		return "__Host-aruma_session"
	}
	return "aruma_session"
}

// New returns the console API.
func New(svc *core.Service, auth *authn.Authenticator, logger *slog.Logger, opts Options) *API {
	return &API{
		svc:  svc,
		auth: auth,
		log:  logger,
		opts: opts,
		// ADR-010 S-6: per-IP bucket on credential endpoints, and per-email escalating backoff.
		ipLimit: ratelimit.NewLimiter(10, 10),
		backoff: ratelimit.NewBackoff(5, 15*time.Minute, 30*time.Second, 15*time.Minute),
		cookie:  auth.CookieName,
	}
}

var (
	anyCred     = authn.Policy{Session: true, AccessToken: true}
	sessionOnly = authn.Policy{Session: true}
)

// Handler returns the routed API. Cross-origin browser writes are refused before routing.
func (a *API) Handler() http.Handler {
	r := &httpx.Router{
		NotFound: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			apierr.Write(w, apierr.New(apierr.NotFound, "Requested entity was not found."))
		}),
		MethodNotAllowed: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			apierr.Write(w, apierr.New(apierr.Unimplemented, "Method not allowed."))
		}),
	}
	a.routes = nil
	public := func(method, pattern string, h http.HandlerFunc) {
		a.routes = append(a.routes, Route{Method: method, Pattern: pattern, Public: true})
		r.Handle(method, pattern, h)
	}
	with := func(p authn.Policy, method, pattern string, h http.HandlerFunc) {
		a.routes = append(a.routes, Route{Method: method, Pattern: pattern})
		r.Handle(method, pattern, a.auth.Require(p, h))
	}

	public("GET", "/console/v1/status", a.status)
	public("POST", "/console/v1/setup", a.setup)
	public("POST", "/console/v1/auth:login", a.login)
	public("POST", "/console/v1/auth:signup", a.signup)
	with(sessionOnly, "POST", "/console/v1/auth:logout", a.logout)

	with(anyCred, "GET", "/console/v1/me", a.me)
	with(sessionOnly, "PATCH", "/console/v1/me", a.updateMe)
	with(sessionOnly, "POST", "/console/v1/me:changePassword", a.changePassword)
	with(sessionOnly, "GET", "/console/v1/me/sessions", a.listSessions)
	with(sessionOnly, "DELETE", "/console/v1/me/sessions/{id}", a.revokeSession)
	with(sessionOnly, "GET", "/console/v1/me/tokens", a.listTokens)
	with(sessionOnly, "POST", "/console/v1/me/tokens", a.createToken)
	with(sessionOnly, "DELETE", "/console/v1/me/tokens/{id}", a.revokeToken)

	a.projectRoutes(with)

	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apierr.Write(w, apierr.New(apierr.PermissionDenied, "Cross-origin request refused."))
	}))
	return cop.Handler(r)
}

// Route is a registered console route. Public routes need no credentials (ADR-010 S-4).
type Route struct {
	Method  string
	Pattern string
	Public  bool
}

// Routes lists the routes registered by the last call to Handler.
func (a *API) Routes() []Route { return a.routes }

func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	httpx.WriteError(w, r, a.log, err)
}

// limited applies the credential-endpoint limits. It reports false after writing a 429.
func (a *API) limited(w http.ResponseWriter, r *http.Request, email string) bool {
	ip := httpx.ClientAddr(r.Context()).String()
	if ok, wait := a.ipLimit.Allow(ip); !ok {
		a.tooMany(w, wait)
		return false
	}
	if email != "" {
		if wait := a.backoff.Wait(normalize(email)); wait > 0 {
			a.tooMany(w, wait)
			return false
		}
	}
	return true
}

func (a *API) tooMany(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
	apierr.Write(w, apierr.New(apierr.ResourceExhausted, "Too many attempts. Try again later."))
}

func (a *API) setSessionCookie(w http.ResponseWriter, res *core.LoginResult) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookie,
		Value:    res.Token,
		Path:     "/",
		Expires:  res.ExpiresAt,
		HttpOnly: true,
		Secure:   a.opts.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *API) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   a.opts.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
}

func client(r *http.Request) core.ClientInfo {
	return core.ClientInfo{IP: httpx.ClientAddr(r.Context()), UserAgent: r.UserAgent()}
}
