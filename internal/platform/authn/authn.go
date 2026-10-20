// Package authn identifies the caller of an API.
//
// There are two kinds of principal: project owners (accounts) and service accounts. Each API
// declares which credentials it accepts (a Policy); one verifier exists per credential type and
// never accepts another's. An API key is not a credential here at all (ADR-010 S-10).
package authn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/crypto"
	"github.com/osama300/aurma-project/internal/platform/httpx"
)

// Token prefixes make every credential recognisable on sight and in secret scanners.
const (
	AccessTokenPrefix  = "aruma_pat_" // personal CLI token of an account
	ServiceTokenPrefix = "aruma_sat_" // one-hour access token of a service account
	CSRFHeader         = "X-Aruma-CSRF"
)

// Kind is who the principal is.
type Kind uint8

const (
	KindAccount Kind = iota + 1
	KindServiceAccount
)

// Method is how the principal proved it.
type Method uint8

const (
	MethodSession Method = iota + 1
	MethodAccessToken
	MethodServiceToken
)

// Principal is the authenticated caller.
type Principal struct {
	Kind   Kind
	Method Method

	AccountID uuid.UUID // KindAccount
	SessionID uuid.UUID // MethodSession

	ServiceAccountID uuid.UUID // KindServiceAccount
	ProjectNumber    int64     // KindServiceAccount: the only project it may touch

	Email string

	// ExpiresAt is when the credential stops being valid; zero when it does not expire.
	// Long-lived connections that authenticated once use it to drop the principal in time.
	ExpiresAt time.Time
}

// ActorType names the principal kind for the audit log.
func (p *Principal) ActorType() string {
	if p.Kind == KindServiceAccount {
		return "service_account"
	}
	return "account"
}

// ActorID is the account or service account id.
func (p *Principal) ActorID() uuid.UUID {
	if p.Kind == KindServiceAccount {
		return p.ServiceAccountID
	}
	return p.AccountID
}

type ctxKey struct{}

// WithPrincipal returns a context carrying p.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the authenticated principal. Handlers behind Middleware always have one.
func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(ctxKey{}).(*Principal)
	return p
}

// Verifier checks credentials. Each method returns an UNAUTHENTICATED error for anything
// unknown, expired, revoked or disabled.
type Verifier interface {
	VerifySession(ctx context.Context, token string) (*Principal, error)
	VerifyAccessToken(ctx context.Context, token string) (*Principal, error)
	VerifyServiceToken(ctx context.Context, token string) (*Principal, error)
}

// Policy lists the credentials an API accepts.
type Policy struct {
	Session      bool
	AccessToken  bool
	ServiceToken bool
}

// Authenticator turns requests into principals according to a policy.
type Authenticator struct {
	Verifier   Verifier
	Keys       *crypto.Keys
	CookieName string
	Logger     *slog.Logger
}

// Require wraps next so it only runs for a caller the policy accepts. Requests that change
// state with a session cookie must also carry the session's CSRF token (ADR-010 S-5).
func (a *Authenticator) Require(p Policy, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, err := a.authenticate(r, p)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="aruma"`)
			apierr.Write(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
	})
}

// Optional attaches the principal when the request carries valid credentials, without
// requiring them. Invalid credentials are ignored rather than rejected.
func (a *Authenticator) Optional(p Policy, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principal, err := a.authenticate(r, p); err == nil {
			r = r.WithContext(WithPrincipal(r.Context(), principal))
		}
		next.ServeHTTP(w, r)
	})
}

var errUnauthenticated = apierr.New(apierr.Unauthenticated,
	"Request is missing required authentication credential. Expected a session or OAuth 2 access token.")

func (a *Authenticator) authenticate(r *http.Request, p Policy) (*Principal, error) {
	ctx := r.Context()
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, token, ok := strings.Cut(h, " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			return nil, errUnauthenticated
		}
		token = strings.TrimSpace(token)
		switch {
		case token == "owner":
			// The Firebase emulators accept this token; Aruma never does (ADR-010 S-3).
			a.Logger.Warn("rejected emulator owner token", "client_ip", httpx.ClientAddr(ctx).String(), "path", r.URL.Path)
			return nil, errUnauthenticated
		case looksLikeJWT(token):
			if unsignedJWT(token) {
				a.Logger.Warn("rejected unsigned JWT (alg none)", "client_ip", httpx.ClientAddr(ctx).String(), "path", r.URL.Path)
			}
			return nil, errUnauthenticated
		case strings.HasPrefix(token, AccessTokenPrefix) && p.AccessToken:
			return a.Verifier.VerifyAccessToken(ctx, token)
		case strings.HasPrefix(token, ServiceTokenPrefix) && p.ServiceToken:
			return a.Verifier.VerifyServiceToken(ctx, token)
		default:
			return nil, errUnauthenticated
		}
	}

	if !p.Session {
		return nil, errUnauthenticated
	}
	c, err := r.Cookie(a.CookieName)
	if err != nil || c.Value == "" {
		return nil, errUnauthenticated
	}
	principal, err := a.Verifier.VerifySession(ctx, c.Value)
	if err != nil {
		return nil, err
	}
	if !safeMethod(r.Method) && !a.Keys.CheckCSRF(c.Value, r.Header.Get(CSRFHeader)) {
		return nil, apierr.New(apierr.PermissionDenied, "Missing or invalid CSRF token.")
	}
	return principal, nil
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func looksLikeJWT(token string) bool {
	return strings.Count(token, ".") == 2 && !strings.HasPrefix(token, "aruma_")
}

func unsignedJWT(token string) bool {
	header, _, _ := strings.Cut(token, ".")
	b, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		return false
	}
	var h struct {
		Alg string `json:"alg"`
	}
	return json.Unmarshal(b, &h) == nil && strings.EqualFold(h.Alg, "none")
}
