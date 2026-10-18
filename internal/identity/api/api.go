// Package api serves Aruma Auth over the Identity Toolkit and Secure Token protocols, at the
// host-prefixed paths the Firebase SDKs call in emulator mode (ADR-014):
//
//	/identitytoolkit.googleapis.com/v1/accounts:*         client calls (web, Android)
//	/www.googleapis.com/identitytoolkit/v3/relyingparty/* the same calls, as iOS names them
//	/securetoken.googleapis.com/v1/token                  refresh
//	/identitytoolkit.googleapis.com/v1/projects/{p}/...   the Admin API (admin.go)
//	/issuers/{project}/..., /session-issuers/{project}/... discovery and JWKS (ADR-004)
//
// Client calls are identified by an API key and answer any origin (CORS *, no credentials).
// Errors use the Identity Toolkit envelope, whose message is the code the SDKs map.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/osama300/aurma-project/internal/identity/core"
	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/platform/ratelimit"
)

// API is the Auth HTTP layer.
type API struct {
	svc  *core.Service
	auth *authn.Authenticator // Admin API callers
	log  *slog.Logger

	ipLimit     *ratelimit.Limiter // every client call, per address
	signupLimit *ratelimit.Limiter // account creation, per address
	mailLimit   *ratelimit.Limiter // email sending (sendOobCode), per address
	backoff     *ratelimit.Backoff // password guesses, per project and email
}

// New returns the Auth API. auth authenticates the Admin API's callers.
func New(svc *core.Service, auth *authn.Authenticator, logger *slog.Logger) *API {
	return &API{
		svc: svc, auth: auth, log: logger,
		ipLimit:     ratelimit.NewLimiter(300, 100),
		signupLimit: ratelimit.NewLimiter(2, 100), // ≈ Firebase's 100 accounts per hour per address
		mailLimit:   ratelimit.NewHourlyLimiter(60, 20),
		backoff:     ratelimit.NewBackoff(10, 15*time.Minute, 30*time.Second, 15*time.Minute),
	}
}

// Handler returns the router for every Auth path.
func (a *API) Handler() http.Handler {
	r := &httpx.Router{
		NotFound: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a.writeError(w, r, &core.Error{Code: "NOT_FOUND", Status: http.StatusNotFound})
		}),
	}
	a.clientRoutes(r)
	a.discoveryRoutes(r)
	a.adminRoutes(r)
	return r
}

// cors marks a client or discovery route as callable from any origin. Identity there is the
// API key or a token in the body, never a cookie, so credentials are not allowed (ADR-014).
func cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Access-Control-Allow-Origin", "*")
		hd.Set("Cross-Origin-Resource-Policy", "cross-origin")
		hd.Add("Vary", "Origin")
		if r.Method == http.MethodOptions {
			hd.Set("Access-Control-Allow-Methods", "GET, POST")
			hd.Set("Access-Control-Allow-Headers", "content-type, x-client-version, x-firebase-client, x-firebase-gmpid, x-firebase-appcheck, x-firebase-locale, x-goog-api-key")
			hd.Set("Access-Control-Max-Age", "3600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// handle registers a CORS route with its preflight.
func handle(r *httpx.Router, method, pattern string, h http.Handler) {
	r.Handle(method, pattern, cors(h))
	r.Handle(http.MethodOptions, pattern, cors(h))
}

// apiKey returns the caller's API key: ?key= as the SDKs send it, or X-Goog-Api-Key.
func apiKey(r *http.Request) string {
	if k := r.URL.Query().Get("key"); k != "" {
		return k
	}
	return r.Header.Get("X-Goog-Api-Key")
}

var errMissingKey = &core.Error{Code: "The request is missing a valid API key.", Status: http.StatusForbidden}

// project resolves the API key of a client call and applies the per-address limit.
func (a *API) project(r *http.Request) (core.Project, error) {
	if ok, _ := a.ipLimit.Allow(ratelimit.AddrKey(httpx.ClientAddr(r.Context()))); !ok {
		return core.Project{}, core.ErrTooManyAttempts
	}
	key := apiKey(r)
	if key == "" {
		return core.Project{}, errMissingKey
	}
	return a.svc.ProjectForKey(r.Context(), key)
}

// decode reads a request body into v. It accepts JSON and form encoding, and both camelCase
// and snake_case field names, as Google's REST mappings do: iOS sends the token grant as JSON
// in camelCase, the web SDK as a form in snake_case. Unknown fields are ignored.
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, httpx.MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return apierr.PayloadTooLarge()
		}
		return err
	}
	fields := map[string]json.RawMessage{}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch {
	case mt == "application/x-www-form-urlencoded":
		form, err := url.ParseQuery(string(body))
		if err != nil {
			return invalidJSON
		}
		for k, vs := range form {
			b, _ := json.Marshal(vs[0])
			fields[camel(k)] = b
		}
	case len(strings.TrimSpace(string(body))) == 0:
		return nil
	default:
		var raw map[string]json.RawMessage
		if json.Unmarshal(body, &raw) != nil {
			return invalidJSON
		}
		for k, val := range raw {
			fields[camel(k)] = val
		}
	}
	b, _ := json.Marshal(fields)
	if err := json.Unmarshal(b, v); err != nil {
		return invalidJSON
	}
	return nil
}

var invalidJSON = &core.Error{Code: "INVALID_JSON", Detail: "Invalid JSON payload received.", Status: http.StatusBadRequest}

// camel turns snake_case into camelCase; camelCase passes through.
func camel(s string) string {
	if !strings.Contains(s, "_") {
		return s
	}
	var b strings.Builder
	up := false
	for _, r := range s {
		switch {
		case r == '_':
			up = true
		case up:
			b.WriteRune(unicode.ToUpper(r))
			up = false
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// optString tells an absent field from an explicit null: the web SDK clears a profile field by
// sending null.
type optString struct {
	Set   bool
	Null  bool
	Value string
}

func (o *optString) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Null = true
		return nil
	}
	return json.Unmarshal(b, &o.Value)
}

func writeJSON(w http.ResponseWriter, v any) { httpx.WriteJSON(w, http.StatusOK, v) }

type errorItem struct {
	Message string `json:"message"`
	Domain  string `json:"domain"`
	Reason  string `json:"reason"`
}

type errorBody struct {
	Error struct {
		Code    int         `json:"code"`
		Message string      `json:"message"`
		Errors  []errorItem `json:"errors"`
		Status  string      `json:"status,omitempty"`
	} `json:"error"`
}

// writeError renders an Auth error in the Identity Toolkit envelope; other API errors keep
// google.rpc.Status, and internal causes are logged, never sent.
func (a *API) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var e *core.Error
	if !errors.As(err, &e) {
		httpx.WriteError(w, r, a.log, err)
		return
	}
	var body errorBody
	body.Error.Code = e.Status
	body.Error.Message = e.Error()
	body.Error.Errors = []errorItem{{Message: e.Error(), Domain: "global", Reason: "invalid"}}
	if e.Status == http.StatusForbidden {
		body.Error.Status = "PERMISSION_DENIED"
	}
	httpx.WriteJSON(w, e.Status, body)
}
