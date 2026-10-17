// Package oauth serves the token endpoints under /oauth2/:
//
//	POST /oauth2/v1/token        service account JWT bearer grant (RFC 7523), the token_uri of
//	                             every key file Aruma issues
//	POST /oauth2/v3/token        refresh_token grant for the firebase CLI (FIREBASE_TOKEN_URL)
//	GET  /oauth2/v1/x509/{email} a service account's public certificates
//	GET  /oauth2/v1/certs        the provider certificates named in key files (none yet)
package oauth

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/osama300/aurma-project/internal/configgen"
	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/platform/ratelimit"
	"github.com/osama300/aurma-project/internal/tenancy/core"
)

const jwtBearer = "urn:ietf:params:oauth:grant-type:jwt-bearer"

// API is the token endpoint handler.
type API struct {
	svc     *core.Service
	log     *slog.Logger
	limiter *ratelimit.Limiter
}

// New returns the token endpoints.
func New(svc *core.Service, logger *slog.Logger) *API {
	return &API{svc: svc, log: logger, limiter: ratelimit.NewLimiter(120, 60)}
}

// Handler returns the routed endpoints.
func (a *API) Handler() http.Handler {
	r := &httpx.Router{NotFound: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		apierr.Write(w, apierr.New(apierr.NotFound, "Requested entity was not found."))
	})}
	r.HandleFunc("POST", "/oauth2/v1/token", a.limited(a.jwtGrant))
	r.HandleFunc("POST", "/oauth2/v3/token", a.limited(a.refreshGrant))
	r.HandleFunc("GET", "/oauth2/v1/x509/{email}", a.x509)
	r.HandleFunc("GET", "/oauth2/v1/certs", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{})
	})
	r.HandleFunc("GET", "/oauth2/v1/auth", func(w http.ResponseWriter, _ *http.Request) {
		apierr.Write(w, apierr.New(apierr.Unimplemented, "Interactive OAuth is not supported. Service accounts use the token endpoint."))
	})
	return r
}

func (a *API) limited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ok, wait := a.limiter.Allow(httpx.ClientAddr(r.Context()).String()); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
			writeOAuth(w, http.StatusTooManyRequests, &core.OAuthError{Code: "slow_down", Description: "Too many requests."})
			return
		}
		next(w, r)
	}
}

func (a *API) jwtGrant(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuth(w, http.StatusBadRequest, &core.OAuthError{Code: "invalid_request", Description: "The request body must be a form."})
		return
	}
	if gt := r.PostForm.Get("grant_type"); gt != jwtBearer {
		writeOAuth(w, http.StatusBadRequest, &core.OAuthError{Code: "unsupported_grant_type", Description: "Use " + jwtBearer + "."})
		return
	}
	res, err := a.svc.ExchangeAssertion(r.Context(), r.PostForm.Get("assertion"), configgen.TokenURL(a.svc.Options().PublicURL))
	a.respond(w, r, res, err)
}

func (a *API) refreshGrant(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeOAuth(w, http.StatusBadRequest, &core.OAuthError{Code: "invalid_request", Description: "The request body must be a form."})
		return
	}
	if gt := r.PostForm.Get("grant_type"); gt != "refresh_token" {
		writeOAuth(w, http.StatusBadRequest, &core.OAuthError{Code: "unsupported_grant_type", Description: "Use refresh_token."})
		return
	}
	res, err := a.svc.ExchangeRefreshToken(r.Context(), r.PostForm.Get("refresh_token"))
	a.respond(w, r, res, err)
}

func (a *API) respond(w http.ResponseWriter, r *http.Request, res *core.TokenResponse, err error) {
	var oe *core.OAuthError
	switch {
	case errors.As(err, &oe):
		writeOAuth(w, http.StatusBadRequest, oe)
	case err != nil:
		httpx.WriteError(w, r, a.log, err)
	default:
		w.Header().Set("Pragma", "no-cache")
		httpx.WriteJSON(w, http.StatusOK, res)
	}
}

func writeOAuth(w http.ResponseWriter, status int, e *core.OAuthError) {
	w.Header().Set("Pragma", "no-cache")
	httpx.WriteJSON(w, status, e)
}

func (a *API) x509(w http.ResponseWriter, r *http.Request) {
	certs, err := a.svc.PublicCertificates(r.Context(), r.PathValue("email"))
	if err != nil {
		httpx.WriteError(w, r, a.log, err)
		return
	}
	if len(certs) == 0 {
		apierr.Write(w, apierr.New(apierr.NotFound, "Requested entity was not found."))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, certs)
}
