package api

import (
	"net/http"

	"github.com/osama300/aurma-project/internal/identity/core"
	"github.com/osama300/aurma-project/internal/platform/httpx"
	"github.com/osama300/aurma-project/internal/platform/ratelimit"
)

// oobRoutes registers the email action calls, sendOobCode and resetPassword under their v1 and
// v3 names, and the action page their links open.
func (a *API) oobRoutes(r *httpx.Router) {
	for _, c := range []struct {
		v1, v3 string
		h      func(http.ResponseWriter, *http.Request, core.Project)
	}{
		{"accounts:sendOobCode", "getOobConfirmationCode", a.sendOobCode},
		{"accounts:resetPassword", "resetPassword", a.resetPassword},
	} {
		h := a.client(c.h)
		handle(r, http.MethodPost, v1+"/"+c.v1, h)
		handle(r, http.MethodPost, v3+"/"+c.v3, h)
	}
	r.Handle(http.MethodGet, core.ActionPath, http.HandlerFunc(a.actionPage))
	r.Handle(http.MethodPost, core.ActionPath, actionFormProtection(http.HandlerFunc(a.actionSubmit)))
}

var errInsufficientPermission = &core.Error{Code: "INSUFFICIENT_PERMISSION", Status: http.StatusForbidden}

func (a *API) sendOobCode(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		RequestType   string `json:"requestType"`
		Email         string `json:"email"`
		NewEmail      string `json:"newEmail"`
		IDToken       string `json:"idToken"`
		ContinueURL   string `json:"continueUrl"`
		ReturnOobLink bool   `json:"returnOobLink"`
		TenantID      string `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	if req.ReturnOobLink {
		// Only the Admin API may receive the link instead of the user (research §3.7).
		a.writeError(w, r, errInsufficientPermission)
		return
	}
	if ok, _ := a.mailLimit.Allow(ratelimit.AddrKey(httpx.ClientAddr(r.Context()))); !ok {
		a.writeError(w, r, core.ErrTooManyAttempts)
		return
	}
	email, err := a.svc.SendOobCode(r.Context(), p, core.OobRequest{
		RequestType: req.RequestType, Email: req.Email, NewEmail: req.NewEmail, IDToken: req.IDToken,
		ContinueURL: req.ContinueURL, Locale: r.Header.Get("X-Firebase-Locale"), APIKey: apiKey(r),
	})
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"kind": "identitytoolkit#GetOobConfirmationCodeResponse", "email": email})
}

// resetPassword inspects a code (no newPassword) or uses a PASSWORD_RESET code to set one.
func (a *API) resetPassword(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		OobCode     string    `json:"oobCode"`
		NewPassword optString `json:"newPassword"`
		TenantID    string    `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	var info core.ActionCodeInfo
	var err error
	if req.NewPassword.Set && !req.NewPassword.Null {
		info, err = a.svc.ResetPassword(r.Context(), p, req.OobCode, req.NewPassword.Value)
	} else {
		info, err = a.svc.CheckActionCode(r.Context(), p, req.OobCode)
	}
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	out := map[string]any{"kind": "identitytoolkit#ResetPasswordResponse", "email": info.Email, "requestType": info.RequestType}
	if info.NewEmail != "" {
		out["newEmail"] = info.NewEmail
	}
	writeJSON(w, out)
}

// applyActionCode is accounts:update with an oobCode: verify, change or recover an email.
func (a *API) applyActionCode(w http.ResponseWriter, r *http.Request, p core.Project, code string) {
	info, user, err := a.svc.ApplyActionCode(r.Context(), p, code, r.Header.Get("X-Firebase-Locale"), apiKey(r))
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	u := toUserJSON(user)
	out := map[string]any{
		"kind": "identitytoolkit#SetAccountInfoResponse", "localId": u.LocalID, "email": u.Email,
		"displayName": u.DisplayName, "photoUrl": u.PhotoURL, "passwordHash": u.PasswordHash,
		"providerUserInfo": u.ProviderUserInfo, "emailVerified": u.EmailVerified,
	}
	if info.RequestType == core.MailChangeEmail {
		out["newEmail"] = info.NewEmail
	}
	writeJSON(w, out)
}
