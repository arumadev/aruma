package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/osama300/aurma-project/internal/identity/core"
	"github.com/osama300/aurma-project/internal/platform/httpx"
)

// idpRoutes registers sign-in with Google and Apple: the REST calls, and the handler and
// iframe pages the web SDK opens for popups and redirects (idp_pages.go). Only these two
// /emulator/ pages exist; the emulator's other routes are never registered (ADR-014).
func (a *API) idpRoutes(r *httpx.Router) {
	h := a.client(a.signInWithIdp)
	handle(r, http.MethodPost, v1+"/accounts:signInWithIdp", h)
	handle(r, http.MethodPost, v3+"/verifyAssertion", h)
	handle(r, http.MethodPost, v2+"/accounts:revokeToken", a.client(a.revokeToken))
	for _, prefix := range []string{"/__/auth", "/emulator/auth"} {
		r.Handle(http.MethodGet, prefix+"/handler", http.HandlerFunc(a.handlerPage))
		r.Handle(http.MethodPost, prefix+"/handler", http.HandlerFunc(a.handlerPage))
		r.Handle(http.MethodGet, prefix+"/iframe", http.HandlerFunc(a.iframePage))
	}
	r.Handle(http.MethodGet, "/__/firebase/init.json", http.HandlerFunc(a.initJSON))
}

// idpAuthURI is accounts:createAuthUri for a provider: it starts a sign-in whose redirect_uri
// is continueUri, which must be on an authorized domain, and returns the provider URL and the
// sessionId to give back to signInWithIdp.
func (a *API) idpAuthURI(w http.ResponseWriter, r *http.Request, p core.Project, req createAuthURIRequest) {
	if req.ContinueURI == "" {
		a.writeError(w, r, &core.Error{Code: "MISSING_CONTINUE_URI", Status: http.StatusBadRequest})
		return
	}
	scopes := strings.FieldsFunc(req.OAuthScope, func(c rune) bool { return c == ',' || c == ' ' })
	started, err := a.svc.StartIdP(r.Context(), p, core.StartInput{
		ProviderID: req.ProviderID, CallbackURL: req.ContinueURI, Scopes: scopes, CustomParameters: req.CustomParameter,
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{
		"kind": "identitytoolkit#CreateAuthUriResponse", "authUri": started.AuthURI,
		"providerId": req.ProviderID, "sessionId": started.SessionID,
	})
}

// createAuthURIRequest is accounts:createAuthUri, for an email or for a provider.
type createAuthURIRequest struct {
	Identifier      string            `json:"identifier"`
	ProviderID      string            `json:"providerId"`
	ContinueURI     string            `json:"continueUri"`
	OAuthScope      string            `json:"oauthScope"`
	CustomParameter map[string]string `json:"customParameter"`
	TenantID        string            `json:"tenantId"`
}

func (a *API) signInWithIdp(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		RequestURI          string `json:"requestUri"`
		PostBody            string `json:"postBody"`
		SessionID           string `json:"sessionId"`
		IDToken             string `json:"idToken"`
		PendingToken        string `json:"pendingToken"`
		ReturnIdpCredential bool   `json:"returnIdpCredential"`
		AutoCreate          *bool  `json:"autoCreate"`
		TenantID            string `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	res, err := a.svc.SignInWithIdP(r.Context(), p, core.IdPInput{
		RequestURI: req.RequestURI, PostBody: req.PostBody, SessionID: req.SessionID, IDToken: req.IDToken,
		PendingToken: req.PendingToken, AutoCreate: req.AutoCreate,
	})
	if err != nil {
		// With returnIdpCredential the SDKs read these errors from a 200 body that also
		// carries the credential, so the app can sign in to the other account and link.
		if res != nil && req.ReturnIdpCredential && core.CredentialError(err) {
			out := idpJSON(res)
			out["errorMessage"] = err.Error()
			writeJSON(w, out)
			return
		}
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, idpJSON(res))
}

// idpJSON is a VerifyAssertionResponse. needConfirmation appears only when it is true: the
// SDKs throw account-exists-with-different-credential for the mere presence of the key.
func idpJSON(res *core.IdPResult) map[string]any {
	out := map[string]any{
		"kind": "identitytoolkit#VerifyAssertionResponse", "providerId": res.ProviderID,
		"federatedId": res.FederatedID, "emailVerified": res.EmailVerified,
	}
	for k, v := range map[string]string{
		"email": res.Email, "firstName": res.FirstName, "lastName": res.LastName, "fullName": res.DisplayName,
		"displayName": res.DisplayName, "photoUrl": res.PhotoURL, "rawUserInfo": res.RawUserInfo,
		"oauthIdToken": res.OAuthIDToken, "oauthAccessToken": res.OAuthAccessToken,
	} {
		if v != "" {
			out[k] = v
		}
	}
	if res.OAuthExpiresIn > 0 {
		out["oauthExpireIn"] = res.OAuthExpiresIn
	}
	if res.NeedConfirmation {
		out["needConfirmation"] = true
		if len(res.VerifiedProviders) > 0 {
			out["verifiedProvider"] = res.VerifiedProviders
		}
		return out
	}
	if s := res.Session; s != nil {
		out["localId"], out["idToken"], out["refreshToken"] = s.User.UID, s.IDToken, s.RefreshToken
		out["expiresIn"], out["isNewUser"] = expiresIn(s), s.IsNewUser
	}
	return out
}

func (a *API) revokeToken(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		ProviderID string `json:"providerId"`
		TokenType  string `json:"tokenType"`
		Token      string `json:"token"`
		IDToken    string `json:"idToken"`
		TenantID   string `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	if req.IDToken == "" {
		a.writeError(w, r, core.ErrInvalidIDToken)
		return
	}
	err := a.svc.RevokeToken(r.Context(), p, core.RevokeInput{ProviderID: req.ProviderID, TokenType: req.TokenType, Token: req.Token, IDToken: req.IDToken})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{})
}

// initJSON is /__/firebase/init.json on a project's own authDomain: the web config of its
// first web app, as Firebase Hosting serves it. The shared default domain names no project.
func (a *API) initJSON(w http.ResponseWriter, r *http.Request) {
	cfg, err := a.svc.WebConfigForHost(r.Context(), hostOnly(r.Host))
	if err != nil {
		a.writeError(w, r, &core.Error{Code: "NOT_FOUND", Status: http.StatusNotFound})
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	b, _ := json.Marshal(cfg)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(b)
}
