package console

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/tenancy/core"
)

type accountJSON struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"displayName"`
	Locale      string    `json:"locale"`
	CreateTime  time.Time `json:"createTime"`
}

func toAccountJSON(a core.Account) accountJSON {
	return accountJSON{ID: a.ID.String(), Email: a.Email, DisplayName: a.DisplayName, Locale: a.Locale, CreateTime: a.CreatedAt}
}

type loginJSON struct {
	Account    accountJSON `json:"account"`
	CSRFToken  string      `json:"csrfToken"`
	ExpireTime time.Time   `json:"expireTime"`
}

type credentialsJSON struct {
	SetupCode   string `json:"setupCode"`
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
	Locale      string `json:"locale"`
}

// maxEmailLen is the longest address RFC 5321 allows.
const maxEmailLen = 254

func normalize(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// status is public: the dashboard reads it before anyone signs in.
func (a *API) status(w http.ResponseWriter, r *http.Request) {
	required, err := a.svc.SetupRequired(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"setupRequired": required,
		"signupOpen":    a.svc.Options().SignupOpen,
		"version":       a.opts.Version,
		"limits":        limits,
	})
}

// limits are the published v1 limits shown on the "v1 limits" page.
var limits = map[string]any{
	"servers":               1,
	"maxServiceAccountKeys": 10,
	"deletedRetentionDays":  30,
}

func (a *API) setup(w http.ResponseWriter, r *http.Request) {
	if !a.limited(w, r) {
		return
	}
	var in credentialsJSON
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.svc.CompleteSetup(r.Context(), in.SetupCode, newAccount(in), client(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.signedIn(w, res)
}

func (a *API) signup(w http.ResponseWriter, r *http.Request) {
	if !a.limited(w, r) {
		return
	}
	var in credentialsJSON
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.svc.Signup(r.Context(), newAccount(in), client(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.signedIn(w, res)
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var in credentialsJSON
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	if !a.limited(w, r) {
		return
	}
	// No real address is longer; refusing early keeps attacker-sized strings out of memory.
	if len(in.Email) > maxEmailLen {
		a.fail(w, r, apierr.Invalid("email", "Enter a valid email address."))
		return
	}
	at := a.attempt(w, "login:"+normalize(in.Email))
	if at == nil {
		return
	}
	res, err := a.svc.Login(r.Context(), in.Email, in.Password, client(r))
	at.Done(err == nil)
	if err != nil {
		if apierr.CodeOf(err) == apierr.Unauthenticated {
			a.log.Info("login failed", "client_ip", httpx.ClientAddr(r.Context()).String())
		}
		a.fail(w, r, err)
		return
	}
	a.signedIn(w, res)
}

func (a *API) signedIn(w http.ResponseWriter, res *core.LoginResult) {
	a.setSessionCookie(w, res)
	httpx.WriteJSON(w, http.StatusOK, loginJSON{Account: toAccountJSON(res.Account), CSRFToken: res.CSRFToken, ExpireTime: res.ExpiresAt})
}

func newAccount(in credentialsJSON) core.NewAccount {
	return core.NewAccount{Email: in.Email, Password: in.Password, DisplayName: in.DisplayName, Locale: in.Locale}
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.Logout(r.Context(), authn.FromContext(r.Context())); err != nil {
		a.fail(w, r, err)
		return
	}
	a.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// me returns the caller's account and, for a session, its CSRF token, so a reloaded page can
// recover it. The response is same-origin JSON without CORS, so other origins cannot read it.
func (a *API) me(w http.ResponseWriter, r *http.Request) {
	p := authn.FromContext(r.Context())
	acc, err := a.svc.Me(r.Context(), p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := map[string]any{"account": toAccountJSON(acc)}
	if c, err := r.Cookie(a.cookie); err == nil && p.Method == authn.MethodSession {
		out["csrfToken"] = a.svc.Keys().CSRFToken(c.Value)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (a *API) updateMe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DisplayName *string `json:"displayName"`
		Locale      *string `json:"locale"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	acc, err := a.svc.UpdateProfile(r.Context(), authn.FromContext(r.Context()), in.DisplayName, in.Locale)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"account": toAccountJSON(acc)})
}

func (a *API) changePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	p := authn.FromContext(r.Context())
	at := a.attempt(w, "account:"+p.AccountID.String())
	if at == nil {
		return
	}
	err := a.svc.ChangePassword(r.Context(), p, in.CurrentPassword, in.NewPassword)
	at.Done(!isCurrentPasswordError(err))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type sessionJSON struct {
	ID         string    `json:"id"`
	Current    bool      `json:"current"`
	IP         string    `json:"ip,omitempty"`
	UserAgent  string    `json:"userAgent,omitempty"`
	CreateTime time.Time `json:"createTime"`
	LastSeen   time.Time `json:"lastSeenTime"`
	ExpireTime time.Time `json:"expireTime"`
}

func (a *API) listSessions(w http.ResponseWriter, r *http.Request) {
	list, err := a.svc.ListSessions(r.Context(), authn.FromContext(r.Context()))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]sessionJSON, 0, len(list))
	for _, s := range list {
		out = append(out, sessionJSON{ID: s.ID.String(), Current: s.Current, IP: s.IP, UserAgent: s.UserAgent, CreateTime: s.CreatedAt, LastSeen: s.LastSeenAt, ExpireTime: s.ExpiresAt})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (a *API) revokeSession(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.svc.RevokeSession(r.Context(), authn.FromContext(r.Context()), id); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type tokenJSON struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	CreateTime time.Time  `json:"createTime"`
	LastUsed   *time.Time `json:"lastUsedTime,omitempty"`
	ExpireTime *time.Time `json:"expireTime,omitempty"`
	Token      string     `json:"token,omitempty"`
}

func toTokenJSON(t core.AccessToken) tokenJSON {
	return tokenJSON{ID: t.ID.String(), Name: t.Name, Prefix: t.Prefix, CreateTime: t.CreatedAt, LastUsed: t.LastUsedAt, ExpireTime: t.ExpiresAt, Token: t.Plaintext}
}

func (a *API) listTokens(w http.ResponseWriter, r *http.Request) {
	list, err := a.svc.ListAccessTokens(r.Context(), authn.FromContext(r.Context()))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := make([]tokenJSON, 0, len(list))
	for _, t := range list {
		out = append(out, toTokenJSON(t))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

func (a *API) createToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name            string `json:"name"`
		ExpireDays      int    `json:"expireDays"`
		CurrentPassword string `json:"currentPassword"`
	}
	if err := httpx.DecodeJSON(r, &in, true); err != nil {
		a.fail(w, r, err)
		return
	}
	if in.ExpireDays < 0 || in.ExpireDays > 366 {
		a.fail(w, r, apierr.Invalid("expireDays", "Token lifetime must be between 1 and 366 days, or 0 for no expiry."))
		return
	}
	// A CLI token outlives the session, so minting one needs the password, not just a session
	// that may have been stolen.
	p := authn.FromContext(r.Context())
	at := a.attempt(w, "account:"+p.AccountID.String())
	if at == nil {
		return
	}
	err := a.svc.CheckPassword(r.Context(), p, in.CurrentPassword)
	at.Done(err == nil)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	t, err := a.svc.CreateAccessToken(r.Context(), p, in.Name, time.Duration(in.ExpireDays)*24*time.Hour)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toTokenJSON(t))
}

func (a *API) revokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.svc.RevokeAccessToken(r.Context(), authn.FromContext(r.Context()), id); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, apierr.New(apierr.NotFound, "Requested entity was not found.")
	}
	return id, nil
}

// isCurrentPasswordError reports whether err rejects the current password, as opposed to the
// new one: only the former counts as a failed attempt.
func isCurrentPasswordError(err error) bool {
	if err == nil {
		return false
	}
	for _, d := range apierr.From(err).Details {
		b, _ := json.Marshal(d)
		if strings.Contains(string(b), `"field":"currentPassword"`) {
			return true
		}
	}
	return false
}
