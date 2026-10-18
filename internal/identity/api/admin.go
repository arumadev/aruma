package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/osama300/aurma-project/internal/identity/core"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/httpx"
)

// The Admin API is what firebase-admin calls, at the paths and shapes both official SDKs use
// (docs/research/phase2-admin-auth.md §3): every call is under /v1/projects/{project}, where
// {project} is the project id or number. The callers are server code, never a browser, so
// there is no CORS. A caller is a service account with an access token from
// /oauth2/v1/token, acting on its own project, or the project owner with a CLI token. API keys,
// ID tokens, "Bearer owner" and anonymous calls are refused by authn before any handler runs.

var adminPolicy = authn.Policy{AccessToken: true, ServiceToken: true}

// Admin errors the SDKs map.
var (
	errMissingLocalID   = &core.Error{Code: "MISSING_LOCAL_ID", Status: http.StatusBadRequest}
	errNotDisabled      = &core.Error{Code: "NOT_DISABLED", Detail: "Disable the account before batch deletion.", Status: http.StatusBadRequest}
	errPhoneUnsupported = &core.Error{Code: "OPERATION_NOT_ALLOWED", Detail: "Phone numbers are not supported yet.", Status: http.StatusBadRequest}
)

// defaultListSize is the page size of accounts:batchGet without maxResults, as Identity Toolkit.
const defaultListSize = 20

// adminRoutes registers the Admin SDK calls under /identitytoolkit.googleapis.com/v1/projects/.
func (a *API) adminRoutes(r *httpx.Router) {
	p := v1 + "/projects/{project}"
	r.Handle(http.MethodPost, p+"/accounts", a.admin(a.adminCreate))
	r.Handle(http.MethodPost, p+"/accounts:lookup", a.admin(a.adminLookup))
	r.Handle(http.MethodPost, p+"/accounts:update", a.admin(a.adminUpdate))
	r.Handle(http.MethodPost, p+"/accounts:delete", a.admin(a.adminDelete))
	r.Handle(http.MethodGet, p+"/accounts:batchGet", a.admin(a.adminList))
	r.Handle(http.MethodPost, p+"/accounts:batchDelete", a.admin(a.adminBatchDelete))
	r.Handle(http.MethodPost, p+"/accounts:sendOobCode", a.admin(a.adminSendOobCode))
	r.Handle(http.MethodPost, p+":createSessionCookie", a.admin(a.adminSessionCookie))
}

// admin authenticates the caller and resolves the project it may act on.
func (a *API) admin(h func(http.ResponseWriter, *http.Request, core.Project)) http.Handler {
	return a.auth.Require(adminPolicy, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := a.svc.ProjectForPrincipal(r.Context(), authn.FromContext(r.Context()), r.PathValue("project"))
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		h(w, r, p)
	}))
}

// flexInt is an integer sent as a JSON number or a string: Node sends validSince as a number,
// Go as a string.
type flexInt struct {
	Set   bool
	Value int64
}

func (f *flexInt) UnmarshalJSON(b []byte) error {
	f.Set = true
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	f.Value = int64(v)
	return nil
}

func (a *API) adminCreate(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		LocalID          string          `json:"localId"`
		Email            *string         `json:"email"`
		Password         *string         `json:"password"`
		DisplayName      *string         `json:"displayName"`
		PhotoURL         *string         `json:"photoUrl"`
		EmailVerified    *bool           `json:"emailVerified"`
		Disabled         *bool           `json:"disabled"`
		CustomAttributes *string         `json:"customAttributes"`
		PhoneNumber      string          `json:"phoneNumber"`
		MfaInfo          json.RawMessage `json:"mfaInfo"`
		TenantID         string          `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	if req.PhoneNumber != "" || len(req.MfaInfo) > 0 {
		a.writeError(w, r, errPhoneUnsupported)
		return
	}
	in := core.AdminUser{
		UID: req.LocalID, Email: req.Email, Password: req.Password, DisplayName: req.DisplayName,
		PhotoURL: req.PhotoURL, EmailVerified: req.EmailVerified, Disabled: req.Disabled,
	}
	if req.CustomAttributes != nil {
		c := json.RawMessage(*req.CustomAttributes)
		in.CustomClaims = &c
	}
	u, err := a.svc.CreateUser(r.Context(), p, in)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"kind": "identitytoolkit#SignupNewUserResponse", "localId": u.UID, "email": u.Email, "displayName": u.DisplayName})
}

// adminLookup is getUser, getUserByEmail and getUsers. Unknown identifiers are skipped, and no
// match at all leaves "users" out, as Identity Toolkit does; the SDKs turn that into
// user-not-found. Phone numbers and federated ids match nobody until those providers exist.
func (a *API) adminLookup(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		LocalID  []string `json:"localId"`
		Email    []string `json:"email"`
		IDToken  string   `json:"idToken"`
		TenantID string   `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	var users []core.User
	if req.IDToken != "" {
		u, err := a.svc.Lookup(r.Context(), p, req.IDToken)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		users = append(users, u)
	}
	if len(req.LocalID)+len(req.Email) > 0 {
		found, err := a.svc.GetUsers(r.Context(), p, req.LocalID, req.Email)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		users = append(users, found...)
	}
	out := map[string]any{"kind": "identitytoolkit#GetAccountInfoResponse"}
	if len(users) > 0 {
		out["users"] = usersJSON(users)
	}
	writeJSON(w, out)
}

// adminUpdate is updateUser, setCustomUserClaims, revokeRefreshTokens and disabling a user.
func (a *API) adminUpdate(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		LocalID              string          `json:"localId"`
		Email                optString       `json:"email"`
		Password             optString       `json:"password"`
		DisplayName          optString       `json:"displayName"`
		PhotoURL             optString       `json:"photoUrl"`
		EmailVerified        *bool           `json:"emailVerified"`
		DisableUser          *bool           `json:"disableUser"`
		CustomAttributes     *string         `json:"customAttributes"`
		ValidSince           flexInt         `json:"validSince"`
		DeleteAttribute      []string        `json:"deleteAttribute"`
		DeleteProvider       []string        `json:"deleteProvider"`
		PhoneNumber          optString       `json:"phoneNumber"`
		LinkProviderUserInfo json.RawMessage `json:"linkProviderUserInfo"`
		Mfa                  json.RawMessage `json:"mfa"`
		TenantID             string          `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	if req.LocalID == "" {
		a.writeError(w, r, errMissingLocalID)
		return
	}
	if (req.PhoneNumber.Set && !req.PhoneNumber.Null) || len(req.LinkProviderUserInfo) > 0 || len(req.Mfa) > 0 {
		a.writeError(w, r, errPhoneUnsupported)
		return
	}
	in := core.AdminUser{
		UID: req.LocalID, EmailVerified: req.EmailVerified, Disabled: req.DisableUser, DeleteProviders: req.DeleteProvider,
		// The SDKs send validSince = now. Any value revokes at the server's clock: moving it
		// back would bring revoked sessions to life.
		RevokeTokens: req.ValidSince.Set,
	}
	for _, f := range []struct {
		opt optString
		dst **string
	}{{req.Email, &in.Email}, {req.Password, &in.Password}, {req.DisplayName, &in.DisplayName}, {req.PhotoURL, &in.PhotoURL}} {
		if f.opt.Set && !f.opt.Null {
			v := f.opt.Value
			*f.dst = &v
		}
	}
	empty := ""
	for _, attr := range req.DeleteAttribute {
		switch attr {
		case "DISPLAY_NAME":
			in.DisplayName = &empty
		case "PHOTO_URL":
			in.PhotoURL = &empty
		}
	}
	if req.CustomAttributes != nil {
		c := json.RawMessage(*req.CustomAttributes)
		in.CustomClaims = &c
	}
	u, err := a.svc.UpdateUser(r.Context(), p, in)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	j := toUserJSON(u)
	writeJSON(w, map[string]any{
		"kind": "identitytoolkit#SetAccountInfoResponse", "localId": j.LocalID, "email": j.Email,
		"displayName": j.DisplayName, "photoUrl": j.PhotoURL, "passwordHash": j.PasswordHash,
		"providerUserInfo": j.ProviderUserInfo, "emailVerified": j.EmailVerified,
	})
}

func (a *API) adminDelete(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		LocalID  string `json:"localId"`
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
	if req.LocalID == "" {
		a.writeError(w, r, errMissingLocalID)
		return
	}
	if err := a.svc.DeleteUser(r.Context(), p, req.LocalID); err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"kind": "identitytoolkit#DeleteAccountResponse"})
}

// adminList is listUsers: pages in uid order, the next page token being opaque to callers.
func (a *API) adminList(w http.ResponseWriter, r *http.Request, p core.Project) {
	q := r.URL.Query()
	size := defaultListSize
	if s := q.Get("maxResults"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 1000 {
			a.writeError(w, r, core.ErrInvalidPageSelection)
			return
		}
		size = n
	}
	users, next, err := a.svc.ListUsers(r.Context(), p, q.Get("nextPageToken"), size)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	out := map[string]any{"kind": "identitytoolkit#DownloadAccountResponse"}
	if len(users) > 0 {
		out["users"] = usersJSON(users)
	}
	if next != "" {
		out["nextPageToken"] = next
	}
	writeJSON(w, out)
}

// adminBatchDelete is deleteUsers. Without force, enabled accounts are refused, as Identity
// Toolkit does; the SDKs always send force.
func (a *API) adminBatchDelete(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		LocalIDs []string `json:"localIds"`
		Force    bool     `json:"force"`
		TenantID string   `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	failed := map[int]error{}
	if !req.Force {
		existing, err := a.svc.GetUsers(r.Context(), p, req.LocalIDs, nil)
		if err != nil {
			a.writeError(w, r, err)
			return
		}
		for i, uid := range req.LocalIDs {
			for _, u := range existing {
				if u.UID == uid && !u.Disabled {
					failed[i] = errNotDisabled
				}
			}
		}
	}
	var uids []string
	var index []int // position in the request of each uid passed on
	for i, uid := range req.LocalIDs {
		if failed[i] == nil {
			uids, index = append(uids, uid), append(index, i)
		}
	}
	res, err := a.svc.DeleteUsers(r.Context(), p, uids)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	for j, e := range res {
		failed[index[j]] = e
	}
	type itemError struct {
		Index   int    `json:"index"`
		LocalID string `json:"localId"`
		Message string `json:"message"`
	}
	errs := []itemError{}
	for i, uid := range req.LocalIDs {
		if e, ok := failed[i]; ok {
			errs = append(errs, itemError{Index: i, LocalID: uid, Message: e.Error()})
		}
	}
	out := map[string]any{"kind": "identitytoolkit#BatchDeleteAccountsResponse"}
	if len(errs) > 0 {
		out["errors"] = errs
	}
	writeJSON(w, out)
}

// adminSendOobCode is generatePasswordResetLink, generateEmailVerificationLink and
// generateVerifyAndChangeEmailLink: the Admin SDKs always ask for the link back (returnOobLink),
// and nothing is emailed.
func (a *API) adminSendOobCode(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		RequestType oobReqType `json:"requestType"`
		Email       string     `json:"email"`
		NewEmail    string     `json:"newEmail"`
		ContinueURL string     `json:"continueUrl"`
		TenantID    string     `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	settings := core.ActionCodeSettings{ContinueURL: req.ContinueURL, Language: r.Header.Get("X-Firebase-Locale")}
	var link string
	var err error
	switch req.RequestType {
	case "PASSWORD_RESET":
		link, err = a.svc.GeneratePasswordResetLink(r.Context(), p, req.Email, settings)
	case "VERIFY_EMAIL":
		link, err = a.svc.GenerateEmailVerificationLink(r.Context(), p, req.Email, settings)
	case "VERIFY_AND_CHANGE_EMAIL":
		link, err = a.svc.GenerateVerifyAndChangeEmailLink(r.Context(), p, req.Email, req.NewEmail, settings)
	case "":
		err = &core.Error{Code: "MISSING_REQ_TYPE", Status: http.StatusBadRequest}
	default:
		err = core.ErrOperationNotAllowed // EMAIL_SIGNIN comes with email-link sign-in, in v1.x
	}
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"kind": "identitytoolkit#GetOobConfirmationCodeResponse", "email": req.Email, "oobLink": link})
}

func (a *API) adminSessionCookie(w http.ResponseWriter, r *http.Request, p core.Project) {
	var req struct {
		IDToken       string  `json:"idToken"`
		ValidDuration flexInt `json:"validDuration"`
		TenantID      string  `json:"tenantId"`
	}
	if err := decode(w, r, &req); err != nil {
		a.writeError(w, r, err)
		return
	}
	if err := tenantID(req.TenantID); err != nil {
		a.writeError(w, r, err)
		return
	}
	// Clamp before converting, so an absurd value cannot overflow into the allowed range.
	secs := min(max(req.ValidDuration.Value, 0), int64(core.MaxCookieDuration.Seconds())+1)
	cookie, err := a.svc.CreateSessionCookie(r.Context(), p, req.IDToken, time.Duration(secs)*time.Second)
	if err != nil {
		a.writeError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"sessionCookie": cookie})
}

func usersJSON(users []core.User) []userJSON {
	out := make([]userJSON, len(users))
	for i, u := range users {
		out[i] = toUserJSON(u)
	}
	return out
}
