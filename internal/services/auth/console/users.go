package console

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/services/auth/api"
	"github.com/osama300/aurma-project/internal/services/auth/core"
	"github.com/osama300/aurma-project/internal/services/auth/queries"
	"github.com/osama300/aurma-project/internal/services/auth/tokens"
)

// authRoutes registers the Users, Sign-in methods and Settings routes.
func (h *handlers) authRoutes() {
	const base = "/console/v1/projects/{project}/auth"
	h.m.Handle(anyCred, "GET", base+"/users", h.listAuthUsers)
	h.m.Handle(anyCred, "POST", base+"/users", h.createAuthUser)
	h.m.Handle(anyCred, "GET", base+"/users/{uid}", h.getAuthUser)
	h.m.Handle(anyCred, "PATCH", base+"/users/{uid}", h.updateAuthUser)
	h.m.Handle(anyCred, "DELETE", base+"/users/{uid}", h.deleteAuthUser)
	h.m.Handle(anyCred, "POST", base+"/users/{uid}:revokeSessions", h.revokeAuthUserSessions)
	h.m.Handle(anyCred, "GET", base+"/config", h.getAuthConfig)
	h.m.Handle(anyCred, "PATCH", base+"/config", h.updateAuthConfig)
	h.m.Handle(anyCred, "GET", base+"/signingKeys", h.listSigningKeys)
	h.m.Handle(anyCred, "POST", base+"/signingKeys:rotate", h.rotateSigningKey)
}

// authUserJSON is a user as the console shows it. The password hash never leaves the server;
// hasPassword says whether there is one.
type authUserJSON struct {
	UID              string             `json:"uid"`
	Email            string             `json:"email,omitempty"`
	EmailVerified    bool               `json:"emailVerified"`
	DisplayName      string             `json:"displayName,omitempty"`
	PhotoURL         string             `json:"photoUrl,omitempty"`
	Disabled         bool               `json:"disabled"`
	Providers        []authProviderJSON `json:"providers"`
	CustomClaims     string             `json:"customClaims,omitempty"`
	HasPassword      bool               `json:"hasPassword"`
	CreateTime       time.Time          `json:"createTime"`
	LastSignInTime   *time.Time         `json:"lastSignInTime,omitempty"`
	LastRefreshTime  *time.Time         `json:"lastRefreshTime,omitempty"`
	TokensValidAfter time.Time          `json:"tokensValidAfterTime"`
}

type authProviderJSON struct {
	ProviderID string `json:"providerId"`
	Email      string `json:"email,omitempty"`
}

func toAuthUserJSON(u core.User) authUserJSON {
	out := authUserJSON{
		UID: u.UID, Email: u.Email, EmailVerified: u.EmailVerified, DisplayName: u.DisplayName, PhotoURL: u.PhotoURL,
		Disabled: u.Disabled, Providers: []authProviderJSON{}, CustomClaims: string(u.CustomClaims), HasPassword: u.HasPassword,
		CreateTime: u.CreatedAt, LastSignInTime: u.LastLoginAt, LastRefreshTime: u.LastRefreshAt, TokensValidAfter: u.TokensValidAfter,
	}
	for _, p := range u.Providers {
		out.Providers = append(out.Providers, authProviderJSON{ProviderID: p.ProviderID, Email: p.Email})
	}
	return out
}

func (h *handlers) listAuthUsers(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	size, _ := strconv.Atoi(q.Get("pageSize"))
	if size <= 0 {
		size = 100
	}
	users, next, total, err := h.svc.SearchUsers(r.Context(), p, q.Get("query"), q.Get("pageToken"), size)
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	out := make([]authUserJSON, 0, len(users))
	for _, u := range users {
		out = append(out, toAuthUserJSON(u))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"users": out, "nextPageToken": next, "totalSize": total})
}

func (h *handlers) createAuthUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UID           string  `json:"uid"`
		Email         *string `json:"email"`
		Password      *string `json:"password"`
		DisplayName   *string `json:"displayName"`
		EmailVerified *bool   `json:"emailVerified"`
		Disabled      *bool   `json:"disabled"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	u, err := h.svc.CreateUser(r.Context(), p, core.AdminUser{
		UID: in.UID, Email: in.Email, Password: in.Password, DisplayName: in.DisplayName, EmailVerified: in.EmailVerified, Disabled: in.Disabled,
	})
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAuthUserJSON(u))
}

func (h *handlers) getAuthUser(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	u, err := h.svc.GetUser(r.Context(), p, r.PathValue("uid"))
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAuthUserJSON(u))
}

// updateAuthUser applies the fields present. customClaims is the claims object as a JSON
// string, as the claims editor holds it; an empty string clears them.
func (h *handlers) updateAuthUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email         *string `json:"email"`
		Password      *string `json:"password"`
		DisplayName   *string `json:"displayName"`
		PhotoURL      *string `json:"photoUrl"`
		EmailVerified *bool   `json:"emailVerified"`
		Disabled      *bool   `json:"disabled"`
		CustomClaims  *string `json:"customClaims"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	upd := core.AdminUser{
		UID: r.PathValue("uid"), Email: in.Email, Password: in.Password, DisplayName: in.DisplayName, PhotoURL: in.PhotoURL,
		EmailVerified: in.EmailVerified, Disabled: in.Disabled,
	}
	if in.CustomClaims != nil {
		claims := json.RawMessage(*in.CustomClaims)
		upd.CustomClaims = &claims
	}
	u, err := h.svc.UpdateUser(r.Context(), p, upd)
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAuthUserJSON(u))
}

func (h *handlers) deleteAuthUser(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	if err := h.svc.DeleteUser(r.Context(), p, r.PathValue("uid")); err != nil {
		h.failAuth(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// revokeAuthUserSessions invalidates the user's refresh tokens and the ID tokens issued so far.
func (h *handlers) revokeAuthUserSessions(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	u, err := h.svc.UpdateUser(r.Context(), p, core.AdminUser{UID: r.PathValue("uid"), RevokeTokens: true})
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAuthUserJSON(u))
}

// authConfigJSON is the project's sign-in settings, with the fixed limits for reference.
type authConfigJSON struct {
	PasswordEnabled          bool           `json:"passwordEnabled"`
	AnonymousEnabled         bool           `json:"anonymousEnabled"`
	ClientSignupEnabled      bool           `json:"clientSignupEnabled"`
	EnumerationProtection    bool           `json:"enumerationProtection"`
	AuthorizedDomains        []string       `json:"authorizedDomains"`
	PasswordMinLength        int32          `json:"passwordMinLength"`
	PasswordRequireLowercase bool           `json:"passwordRequireLowercase"`
	PasswordRequireUppercase bool           `json:"passwordRequireUppercase"`
	PasswordRequireNumeric   bool           `json:"passwordRequireNumeric"`
	PasswordRequireSymbol    bool           `json:"passwordRequireSymbol"`
	PasswordEnforcement      string         `json:"passwordEnforcement"`
	AppName                  string         `json:"appName"`
	UpdateTime               time.Time      `json:"updateTime"`
	Limits                   authLimitsJSON `json:"limits"`
}

// authLimitsJSON are the limits no setting changes (seconds unless named otherwise).
type authLimitsJSON struct {
	IDTokenLifetime        int64            `json:"idTokenLifetimeSeconds"`
	SessionCookieMax       int64            `json:"sessionCookieMaxSeconds"`
	SigningKeyRotationDays int64            `json:"signingKeyRotationDays"`
	RequestsPerMinute      int              `json:"requestsPerMinute"`
	RequestBurst           int              `json:"requestBurst"`
	SignUpsPerMinute       int              `json:"signUpsPerMinute"`
	SignUpBurst            int              `json:"signUpBurst"`
	PasswordFailures       int              `json:"passwordFailures"`
	PasswordFailureWindow  int64            `json:"passwordFailureWindowSeconds"`
	PasswordBackoffMax     int64            `json:"passwordBackoffMaxSeconds"`
	EmailCodeLifetimes     map[string]int64 `json:"emailCodeLifetimeSeconds"` // by template type
}

func toAuthConfigJSON(c queries.AuthProjectConfig) authConfigJSON {
	l := api.ClientLimits
	codeLifetimes := map[string]int64{}
	for kind, d := range core.EmailCodeLifetimes() {
		codeLifetimes[kind] = int64(d.Seconds())
	}
	return authConfigJSON{
		PasswordEnabled: c.PasswordEnabled, AnonymousEnabled: c.AnonymousEnabled, ClientSignupEnabled: c.ClientSignupEnabled,
		EnumerationProtection: c.EnumerationProtection, AuthorizedDomains: c.AuthorizedDomains,
		PasswordMinLength: c.PasswordMinLength, PasswordRequireLowercase: c.PasswordRequireLower,
		PasswordRequireUppercase: c.PasswordRequireUpper, PasswordRequireNumeric: c.PasswordRequireDigit,
		PasswordRequireSymbol: c.PasswordRequireSymbol, PasswordEnforcement: c.PasswordEnforcement, AppName: c.AppName,
		UpdateTime: c.UpdatedAt,
		Limits: authLimitsJSON{
			IDTokenLifetime: int64(core.TokenLifetime.Seconds()), SessionCookieMax: int64(tokens.SessionCookie.Lifetime().Seconds()),
			SigningKeyRotationDays: int64(tokens.RotateEvery.Hours() / 24),
			RequestsPerMinute:      l.RequestsPerMinute, RequestBurst: l.RequestBurst, SignUpsPerMinute: l.SignUpsPerMinute, SignUpBurst: l.SignUpBurst,
			PasswordFailures: l.PasswordFailures, PasswordFailureWindow: int64(l.FailureWindow.Seconds()), PasswordBackoffMax: int64(l.BackoffMax.Seconds()),
			EmailCodeLifetimes: codeLifetimes,
		},
	}
}

func (h *handlers) getAuthConfig(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	c, err := h.svc.Config(r.Context(), p)
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAuthConfigJSON(c))
}

// updateAuthConfig changes the settings present in the body.
func (h *handlers) updateAuthConfig(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PasswordEnabled          *bool     `json:"passwordEnabled"`
		AnonymousEnabled         *bool     `json:"anonymousEnabled"`
		ClientSignupEnabled      *bool     `json:"clientSignupEnabled"`
		EnumerationProtection    *bool     `json:"enumerationProtection"`
		AuthorizedDomains        *[]string `json:"authorizedDomains"`
		PasswordMinLength        *int32    `json:"passwordMinLength"`
		PasswordRequireLowercase *bool     `json:"passwordRequireLowercase"`
		PasswordRequireUppercase *bool     `json:"passwordRequireUppercase"`
		PasswordRequireNumeric   *bool     `json:"passwordRequireNumeric"`
		PasswordRequireSymbol    *bool     `json:"passwordRequireSymbol"`
		PasswordEnforcement      *string   `json:"passwordEnforcement"`
		AppName                  *string   `json:"appName"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	c, err := h.svc.UpdateConfig(r.Context(), p, func(c *queries.AuthProjectConfig) error {
		set(&c.PasswordEnabled, in.PasswordEnabled)
		set(&c.AnonymousEnabled, in.AnonymousEnabled)
		set(&c.ClientSignupEnabled, in.ClientSignupEnabled)
		set(&c.EnumerationProtection, in.EnumerationProtection)
		set(&c.AuthorizedDomains, in.AuthorizedDomains)
		set(&c.PasswordMinLength, in.PasswordMinLength)
		set(&c.PasswordRequireLower, in.PasswordRequireLowercase)
		set(&c.PasswordRequireUpper, in.PasswordRequireUppercase)
		set(&c.PasswordRequireDigit, in.PasswordRequireNumeric)
		set(&c.PasswordRequireSymbol, in.PasswordRequireSymbol)
		set(&c.PasswordEnforcement, in.PasswordEnforcement)
		set(&c.AppName, in.AppName)
		return nil
	})
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAuthConfigJSON(c))
}

// set copies *v into dst when the field was present.
func set[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

type signingKeyJSON struct {
	Kid          string     `json:"kid"`
	Purpose      string     `json:"purpose"`
	State        string     `json:"state"`
	CreateTime   time.Time  `json:"createTime"`
	ActivateTime *time.Time `json:"activateTime,omitempty"`
	RetireTime   *time.Time `json:"retireTime,omitempty"`
}

func (h *handlers) writeSigningKeys(w http.ResponseWriter, r *http.Request, p core.Project) {
	keys, err := h.svc.SigningKeys(r.Context(), p)
	if err != nil {
		h.failAuth(w, r, err)
		return
	}
	out := make([]signingKeyJSON, 0, len(keys))
	for _, k := range keys {
		out = append(out, signingKeyJSON{Kid: k.Kid, Purpose: string(k.Purpose), State: k.State, CreateTime: k.CreatedAt, ActivateTime: k.ActivatedAt, RetireTime: k.RetiredAt})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (h *handlers) listSigningKeys(w http.ResponseWriter, r *http.Request) {
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	h.writeSigningKeys(w, r, p)
}

// rotateSigningKey rotates the keys of one purpose and answers with the new list.
func (h *handlers) rotateSigningKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Purpose   string `json:"purpose"`
		Emergency bool   `json:"emergency"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		h.m.Fail(w, r, err)
		return
	}
	p, err := h.project(r)
	if err != nil {
		h.m.Fail(w, r, err)
		return
	}
	if err := h.svc.RotateSigningKey(r.Context(), p, in.Purpose, in.Emergency); err != nil {
		h.failAuth(w, r, err)
		return
	}
	h.writeSigningKeys(w, r, p)
}
