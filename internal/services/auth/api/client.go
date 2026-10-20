package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/osama300/aurma-project/internal/platform/crypto"
	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/platform/ratelimit"
	"github.com/osama300/aurma-project/internal/services/auth/core"
)

const (
	v1 = "/identitytoolkit.googleapis.com/v1"
	v2 = "/identitytoolkit.googleapis.com/v2"
	v3 = "/www.googleapis.com/identitytoolkit/v3/relyingparty"
)

// clientRoutes registers the client calls under their v1 names and their v3 aliases: one
// implementation behind both, as the emulator does (research §4).
func (a *API) clientRoutes(r *httpx.Router) {
	calls := []struct {
		v1, v3 string
		h      func(http.ResponseWriter, *http.Request, core.Project)
	}{
		{"accounts:signUp", "signupNewUser", a.signUp},
		{"accounts:signInWithPassword", "verifyPassword", a.signInWithPassword},
		{"accounts:signInWithCustomToken", "verifyCustomToken", a.signInWithCustomToken},
		{"accounts:lookup", "getAccountInfo", a.lookup},
		{"accounts:update", "setAccountInfo", a.update},
		{"accounts:delete", "deleteAccount", a.deleteAccount},
		{"accounts:createAuthUri", "createAuthUri", a.createAuthURI},
	}
	for _, c := range calls {
		h := a.client(c.h)
		handle(r, http.MethodPost, v1+"/"+c.v1, h)
		handle(r, http.MethodPost, v3+"/"+c.v3, h)
	}
	projectConfig := a.client(a.projectConfig)
	handle(r, http.MethodGet, v1+"/projects", projectConfig)
	handle(r, http.MethodGet, v3+"/getProjectConfig", projectConfig)
	handle(r, http.MethodPost, v3+"/getProjectConfig", projectConfig)
	handle(r, http.MethodGet, v2+"/passwordPolicy", a.client(a.passwordPolicy))
	handle(r, http.MethodGet, v2+"/recaptchaConfig", a.client(a.recaptchaConfig))
	handle(r, http.MethodPost, "/securetoken.googleapis.com/v1/token", a.client(a.token))
	// Phone, multi-factor and email-link sign-in come in v1.x. Until then they answer as a
	// project with the provider disabled does, so the SDKs fail cleanly (AC-A17).
	notAllowed := a.client(func(w http.ResponseWriter, r *http.Request, _ core.Project) {
		a.writeError(w, r, core.ErrOperationNotAllowed)
	})
	for _, path := range []string{
		v1 + "/accounts:signInWithPhoneNumber", v1 + "/accounts:sendVerificationCode", v1 + "/accounts:signInWithEmailLink",
		v2 + "/accounts/mfaEnrollment:start", v2 + "/accounts/mfaEnrollment:finalize", v2 + "/accounts/mfaEnrollment:withdraw",
		v2 + "/accounts/mfaSignIn:start", v2 + "/accounts/mfaSignIn:finalize",
		v3 + "/verifyPhoneNumber", v3 + "/sendVerificationCode", v3 + "/emailLinkSignin",
	} {
		handle(r, http.MethodPost, path, notAllowed)
	}
	// The web RecaptchaVerifier reads its site key before any phone call (iOS: getRecaptchaParam).
	// Unanswered, the SDK would report a network error instead of the disabled provider.
	handle(r, http.MethodGet, v1+"/recaptchaParams", notAllowed)
	handle(r, http.MethodGet, v3+"/getRecaptchaParam", notAllowed)
	a.oobRoutes(r)
	a.idpRoutes(r)
}

// client resolves the API key before the handler runs.
func (a *API) client(h func(http.ResponseWriter, *http.Request, core.Project)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.project(r)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		h(w, r, p)
	})
}

// tenantID rejects multi-tenancy, which Aruma does not offer.
func tenantID(t string) error {
	if t != "" {
		return &core.Error{Code: "INVALID_TENANT_ID", Status: http.StatusBadRequest}
	}
	return nil
}

func expiresIn(s *core.Session) string { return strconv.Itoa(s.ExpiresIn) }

func (a *API) signUp(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"displayName"`
		IDToken     string `json:"idToken"`
		TenantID    string `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	// Links count too: signUp answers EMAIL_EXISTS before it looks at the idToken, so an
	// unlimited link would be an unthrottled way to test which addresses are taken.
	if ok, _ := a.signupLimit.Allow(ratelimit.AddrKey(httpx.ClientAddr(r.Context()))); !ok {
		a.writeError(w, r, core.ErrTooManyAttempts)
		return
	}
	s, err := a.svc.SignUp(r.Context(), p, core.SignUpInput{Email: req.Email, Password: req.Password, DisplayName: req.DisplayName, IDToken: req.IDToken})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{
		"kind": "identitytoolkit#SignupNewUserResponse", "localId": s.User.UID, "email": s.User.Email,
		"displayName": s.User.DisplayName, "idToken": s.IDToken, "refreshToken": s.RefreshToken, "expiresIn": expiresIn(s),
	})
}

func (a *API) signInWithPassword(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		TenantID string `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	// The backoff key is the project and the address as typed, lower-cased, so it holds before
	// the email is even validated.
	attempt, _ := a.backoff.Begin(strconv.FormatInt(p.Number, 10) + "|" + strings.ToLower(strings.TrimSpace(req.Email)))
	if attempt == nil {
		a.writeError(w, r, core.ErrTooManyAttempts)
		return
	}
	s, err := a.svc.SignInWithPassword(r.Context(), p, req.Email, req.Password)
	attempt.Done(err == nil)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{
		"kind": "identitytoolkit#VerifyPasswordResponse", "localId": s.User.UID, "email": s.User.Email,
		"displayName": s.User.DisplayName, "idToken": s.IDToken, "registered": true,
		"refreshToken": s.RefreshToken, "expiresIn": expiresIn(s),
	})
}

func (a *API) signInWithCustomToken(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		Token    string `json:"token"`
		TenantID string `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	s, err := a.svc.SignInWithCustomToken(r.Context(), p, req.Token)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{
		"kind": "identitytoolkit#VerifyCustomTokenResponse", "idToken": s.IDToken,
		"refreshToken": s.RefreshToken, "expiresIn": expiresIn(s), "isNewUser": s.IsNewUser,
	})
}

func (a *API) lookup(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		IDToken string `json:"idToken"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if req.IDToken == "" {
		a.writeError(w, r, core.ErrInvalidIDToken)
		return
	}
	u, err := a.svc.Lookup(r.Context(), p, req.IDToken)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"kind": "identitytoolkit#GetAccountInfoResponse", "users": []userJSON{toUserJSON(u)}})
}

func (a *API) update(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		IDToken         string    `json:"idToken"`
		OobCode         string    `json:"oobCode"`
		Email           optString `json:"email"`
		Password        optString `json:"password"`
		DisplayName     optString `json:"displayName"`
		PhotoURL        optString `json:"photoUrl"`
		DeleteAttribute []string  `json:"deleteAttribute"`
		DeleteProvider  []string  `json:"deleteProvider"`
		TenantID        string    `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	if req.OobCode != "" {
		a.applyActionCode(w, r, p, req.OobCode)
		return
	}
	if req.IDToken == "" {
		a.writeError(w, r, core.ErrInvalidReqType)
		return
	}
	in := core.UpdateInput{IDToken: req.IDToken, DeleteAttribute: req.DeleteAttribute, DeleteProvider: req.DeleteProvider}
	if req.Email.Set && !req.Email.Null {
		in.Email = &req.Email.Value
	}
	if req.Password.Set && !req.Password.Null {
		in.Password = &req.Password.Value
	}
	for _, f := range []struct {
		opt  optString
		dst  **string
		attr string
	}{{req.DisplayName, &in.DisplayName, "DISPLAY_NAME"}, {req.PhotoURL, &in.PhotoURL, "PHOTO_URL"}} {
		switch {
		case f.opt.Null || (f.opt.Set && f.opt.Value == ""):
			in.DeleteAttribute = append(in.DeleteAttribute, f.attr)
		case f.opt.Set:
			v := f.opt.Value
			*f.dst = &v
		}
	}
	res, err := a.svc.Update(r.Context(), p, in)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	u := toUserJSON(res.User)
	out := map[string]any{
		"kind": "identitytoolkit#SetAccountInfoResponse", "localId": u.LocalID, "email": u.Email,
		"displayName": u.DisplayName, "photoUrl": u.PhotoURL, "passwordHash": u.PasswordHash,
		"providerUserInfo": u.ProviderUserInfo, "emailVerified": u.EmailVerified,
	}
	if s := res.Session; s != nil {
		out["idToken"], out["refreshToken"], out["expiresIn"] = s.IDToken, s.RefreshToken, expiresIn(s)
	}
	writeJSON(w, out)
}

func (a *API) deleteAccount(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		IDToken string `json:"idToken"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if req.IDToken == "" {
		a.writeError(w, r, core.ErrInvalidIDToken)
		return
	}
	if err := a.svc.Delete(r.Context(), p, req.IDToken); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"kind": "identitytoolkit#DeleteAccountResponse"})
}

func (a *API) createAuthURI(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req createAuthURIRequest
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	if req.ProviderID != "" {
		a.idpAuthURI(w, r, p, req)
		return
	}
	methods, registered, err := a.svc.SignInMethods(r.Context(), p, req.Identifier)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	out := map[string]any{"kind": "identitytoolkit#CreateAuthUriResponse", "sessionId": crypto.RandomString(18)}
	if registered != nil {
		out["registered"] = *registered
	}
	if len(methods) > 0 {
		out["signinMethods"], out["allProviders"] = methods, methods
	}
	writeJSON(w, out)
}

func (a *API) projectConfig(w http.ResponseWriter, r *http.Request, p core.Project) {
	cfg, err := a.svc.Config(r.Context(), p)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	// The project number, as the emulator answers and the token response's project_id is.
	writeJSON(w, map[string]any{"projectId": strconv.FormatInt(p.Number, 10), "authorizedDomains": cfg.AuthorizedDomains})
}

// allowedSymbols are the characters that count as non-alphanumeric in the password policy.
var allowedSymbols = strings.Split("^ $ * . [ ] { } ( ) ? \" ! @ # % & / \\ , > < ' : ; | _ ~ ` = + -", " ")

func (a *API) passwordPolicy(w http.ResponseWriter, r *http.Request, p core.Project) {
	cfg, err := a.svc.Config(r.Context(), p)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{
		"customStrengthOptions": map[string]any{
			// The server takes up to crypto.MaxPasswordLen bytes; 256 characters fit in it whatever
			// the script, so a client that validates against this policy is never rejected.
			"minPasswordLength": cfg.PasswordMinLength, "maxPasswordLength": 256,
			"containsLowercaseCharacter": cfg.PasswordRequireLower, "containsUppercaseCharacter": cfg.PasswordRequireUpper,
			"containsNumericCharacter": cfg.PasswordRequireDigit, "containsNonAlphanumericCharacter": cfg.PasswordRequireSymbol,
		},
		"allowedNonAlphanumericCharacters": allowedSymbols,
		"enforcementState":                 cfg.PasswordEnforcement,
		"forceUpgradeOnSignin":             false,
		"schemaVersion":                    1,
	})
}

// recaptchaConfig answers OFF for every provider: Aruma relies on server-side limits and never
// asks a client for a reCAPTCHA token (ADR-014).
func (a *API) recaptchaConfig(w http.ResponseWriter, r *http.Request, p core.Project) {
	writeJSON(w, map[string]any{"recaptchaEnforcementState": []map[string]string{
		{"provider": "EMAIL_PASSWORD_PROVIDER", "enforcementState": "OFF"},
		{"provider": "PHONE_PROVIDER", "enforcementState": "OFF"},
	}})
}

// token is the Secure Token grant. access_token is the ID token, as iOS needs it non-empty;
// project_id is the project number (research §3.14).
func (a *API) token(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		GrantType    string `json:"grantType"`
		RefreshToken string `json:"refreshToken"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	switch {
	case req.GrantType == "":
		a.writeError(w, r, core.ErrMissingGrantType)
		return
	case req.GrantType != "refresh_token":
		a.writeError(w, r, core.ErrInvalidGrantType)
		return
	}
	s, err := a.svc.Refresh(r.Context(), p, req.RefreshToken)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{
		"access_token": s.IDToken, "expires_in": expiresIn(s), "token_type": "Bearer",
		"refresh_token": s.RefreshToken, "id_token": s.IDToken, "user_id": s.User.UID,
		"project_id": strconv.FormatInt(p.Number, 10),
	})
}
