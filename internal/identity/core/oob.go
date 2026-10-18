package core

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/identity/queries"
	"github.com/osama300/aurma-project/internal/platform/crypto"
	"github.com/osama300/aurma-project/internal/store"
	tcore "github.com/osama300/aurma-project/internal/tenancy/core"
)

// Email action codes (research §3.5, §3.7, §3.8, §6). A code is 256 random bits that exists
// only in its link; the database keeps its HMAC. It is used once and expires.

// ActionPath is the action page every link opens (research §6).
const ActionPath = "/__/auth/action"

// oobLifetimes: Firebase documents none (research §6). The reset link grants a credential, so
// it lives an hour; links that prove or restore an address live three days, the figure
// commonly reported for verification. Recovery shares it: long enough for the previous owner
// to notice the change, short enough to close the window for whoever holds the old mailbox.
var oobLifetimes = map[string]time.Duration{
	MailPasswordReset: time.Hour,
	MailVerifyEmail:   72 * time.Hour,
	MailChangeEmail:   72 * time.Hour,
	MailRecoverEmail:  72 * time.Hour,
}

// oobModes maps a request type to the link's mode (research §6).
var oobModes = map[string]string{
	MailPasswordReset: "resetPassword",
	MailVerifyEmail:   "verifyEmail",
	MailChangeEmail:   "verifyAndChangeEmail",
	MailRecoverEmail:  "recoverEmail",
}

// Errors of the email actions.
var (
	ErrMissingReqType     = fail("MISSING_REQ_TYPE")
	ErrMissingOobCode     = fail("MISSING_OOB_CODE")
	ErrInvalidOobCode     = fail("INVALID_OOB_CODE")
	ErrExpiredOobCode     = fail("EXPIRED_OOB_CODE")
	ErrInvalidContinueURI = fail("INVALID_CONTINUE_URI")
	ErrUnauthorizedDomain = fail("UNAUTHORIZED_DOMAIN")
	ErrResetLimit         = fail("RESET_PASSWORD_EXCEED_LIMIT")
	ErrMissingNewEmail    = fail("MISSING_NEW_EMAIL")
)

// OobRequest is accounts:sendOobCode from a client.
type OobRequest struct {
	RequestType string // PASSWORD_RESET, VERIFY_EMAIL, VERIFY_AND_CHANGE_EMAIL (EMAIL_SIGNIN is refused)
	Email       string // PASSWORD_RESET
	NewEmail    string // VERIFY_AND_CHANGE_EMAIL
	IDToken     string // VERIFY_EMAIL, VERIFY_AND_CHANGE_EMAIL
	ContinueURL string
	Locale      string // X-Firebase-Locale; picks the email's language
	APIKey      string // the caller's key, which the link carries to the action page
}

// ActionCodeInfo describes a code: what resetPassword answers, and what the action page shows.
// ContinueURL is set only while its domain is still authorised.
type ActionCodeInfo struct {
	RequestType string
	Email       string
	NewEmail    string
	ContinueURL string
}

// SendOobCode issues a code and emails its link. Under enumeration protection (the default) an
// unknown address for PASSWORD_RESET, or a taken one for VERIFY_AND_CHANGE_EMAIL, succeeds
// without sending anything, as Identity Platform documents. It returns the response's email.
func (s *Service) SendOobCode(ctx context.Context, p Project, in OobRequest) (string, error) {
	switch in.RequestType {
	case "":
		return "", ErrMissingReqType
	case "EMAIL_SIGNIN":
		return "", ErrOperationNotAllowed // email-link sign-in comes later (AC-A17)
	case MailPasswordReset, MailVerifyEmail, MailChangeEmail:
	default:
		return "", failf(ErrInvalidReqType.Code, "unsupported requestType")
	}
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return "", err
	}
	if err := checkContinueURL(cfg, in.ContinueURL); err != nil {
		return "", err
	}
	in.Locale = mailLanguage(in.Locale)
	switch in.RequestType {
	case MailPasswordReset:
		return s.sendPasswordReset(ctx, p, cfg, in)
	case MailVerifyEmail:
		return s.sendVerifyEmail(ctx, p, cfg, in)
	default:
		return s.sendChangeEmail(ctx, p, cfg, in)
	}
}

func (s *Service) sendPasswordReset(ctx context.Context, p Project, cfg queries.AuthProjectConfig, in OobRequest) (string, error) {
	if in.Email == "" {
		return "", ErrMissingEmail
	}
	email, ok := normalizeEmail(in.Email)
	if !ok {
		return "", ErrInvalidEmail
	}
	if !cfg.PasswordEnabled {
		return "", ErrOperationNotAllowed
	}
	if err := s.allowMail(p, email, ErrResetLimit); err != nil {
		return "", err
	}
	raw, err := s.q.GetUserByEmail(ctx, queries.GetUserByEmailParams{ProjectNumber: p.Number, TenantID: p.Tenant, Email: email})
	if errors.Is(err, pgx.ErrNoRows) {
		if cfg.EnumerationProtection {
			return email, nil
		}
		return "", ErrEmailNotFound
	}
	if err != nil {
		return "", err
	}
	if raw.Disabled {
		if cfg.EnumerationProtection {
			return email, nil
		}
		return "", ErrUserDisabled
	}
	u, err := s.loadUser(ctx, s.q, p, raw)
	if err != nil {
		return "", err
	}
	_, err = s.sendCode(ctx, p, cfg, codeRequest{
		kind: MailPasswordReset, user: u, email: u.Email, to: u.Email, continueURL: in.ContinueURL, lang: in.Locale, apiKey: in.APIKey,
	}, true)
	return email, err
}

func (s *Service) sendVerifyEmail(ctx context.Context, p Project, cfg queries.AuthProjectConfig, in OobRequest) (string, error) {
	if in.IDToken == "" {
		return "", ErrInvalidIDToken
	}
	u, _, err := s.VerifyIDToken(ctx, p, in.IDToken)
	if err != nil {
		return "", err
	}
	if u.Email == "" {
		return "", ErrMissingEmail
	}
	if err := s.allowMail(p, u.Email, ErrTooManyAttempts); err != nil {
		return "", err
	}
	_, err = s.sendCode(ctx, p, cfg, codeRequest{
		kind: MailVerifyEmail, user: u, email: u.Email, to: u.Email, continueURL: in.ContinueURL, lang: in.Locale, apiKey: in.APIKey,
	}, true)
	return u.Email, err
}

// sendChangeEmail is verifyBeforeUpdateEmail: the address changes only when the link sent to
// the new one is opened. Like a direct email change, it needs a recent sign-in.
func (s *Service) sendChangeEmail(ctx context.Context, p Project, cfg queries.AuthProjectConfig, in OobRequest) (string, error) {
	if in.NewEmail == "" {
		return "", ErrMissingNewEmail
	}
	if in.IDToken == "" {
		return "", ErrInvalidIDToken
	}
	newEmail, ok := normalizeEmail(in.NewEmail)
	if !ok {
		return "", ErrInvalidEmail
	}
	u, claims, err := s.VerifyIDToken(ctx, p, in.IDToken)
	if err != nil {
		return "", err
	}
	if err := s.requireRecent(claims); err != nil {
		return "", err
	}
	if err := s.allowMail(p, newEmail, ErrTooManyAttempts); err != nil {
		return "", err
	}
	if _, err := s.q.GetUserByEmail(ctx, queries.GetUserByEmailParams{ProjectNumber: p.Number, TenantID: p.Tenant, Email: newEmail}); err == nil {
		if cfg.EnumerationProtection {
			return u.Email, nil
		}
		return "", ErrEmailExists
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	_, err = s.sendCode(ctx, p, cfg, codeRequest{
		kind: MailChangeEmail, user: u, email: u.Email, newEmail: newEmail, to: newEmail,
		continueURL: in.ContinueURL, lang: in.Locale, apiKey: in.APIKey,
	}, true)
	return u.Email, err
}

// codeRequest is a code to issue and, optionally, the email that carries it.
type codeRequest struct {
	kind        string
	user        User
	email       string // the address the code acts on (the old one for a change)
	newEmail    string
	to          string
	continueURL string
	lang        string
	apiKey      string
}

// sendCode issues a code, and queues its email when send is set, in one transaction.
func (s *Service) sendCode(ctx context.Context, p Project, cfg queries.AuthProjectConfig, in codeRequest, send bool) (string, error) {
	prof, err := s.control.ProjectProfile(ctx, p.Number)
	if err != nil {
		return "", err
	}
	var link string
	err = s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		link, err = s.issueCode(ctx, q, p, cfg, prof, in, send)
		return err
	})
	if err != nil {
		return "", err
	}
	s.audit(ctx, s.q, p, in.user.UID, "oob.issue", "ok", map[string]any{"type": in.kind, "emailed": send})
	return link, nil
}

func (s *Service) issueCode(ctx context.Context, q *queries.Queries, p Project, cfg queries.AuthProjectConfig, prof tcore.ProjectProfile, in codeRequest, send bool) (string, error) {
	code := crypto.RandomString(crypto.TokenBytes)
	uid := in.user.UID
	err := q.CreateOobCode(ctx, queries.CreateOobCodeParams{
		CodeHash: s.oobHash(code), ProjectNumber: p.Number, TenantID: p.Tenant, Uid: &uid, RequestType: in.kind,
		Email: in.email, NewEmail: ptr(in.newEmail), ContinueUrl: ptr(in.continueURL), ExpiresAt: s.now().Add(oobLifetimes[in.kind]),
	})
	if err != nil {
		return "", err
	}
	link := s.actionLink(prof, in.apiKey, oobModes[in.kind], code, in.continueURL, in.lang)
	if send {
		err = s.enqueue(ctx, q, p, cfg, in.kind, in.lang, in.to, mailVars{
			Link: link, AppName: appName(cfg, prof), Email: in.email, NewEmail: in.newEmail, DisplayName: in.user.DisplayName,
		})
	}
	return link, err
}

func (s *Service) oobHash(code string) []byte { return s.keys.HashToken("auth-oob:" + code) }

// ActionFormToken is the anti-forgery token of the action page's form for a code. It is bound
// to the code, so only a page that already holds the code can produce it.
func (s *Service) ActionFormToken(code string) string {
	return base64.RawURLEncoding.EncodeToString(s.keys.HashToken("auth-action-form:" + code))
}

// CheckActionFormToken reports, in constant time, whether token belongs to code.
func (s *Service) CheckActionFormToken(code, token string) bool {
	return subtle.ConstantTimeCompare([]byte(s.ActionFormToken(code)), []byte(token)) == 1
}

// actionLink builds https://<authDomain>/__/auth/action?mode=…&oobCode=…&apiKey=…&lang=…
// [&continueUrl=…]. A project on the default auth domain (the host of public_url) gets the
// public URL itself, port and scheme included; a custom auth domain is reached over https.
func (s *Service) actionLink(prof tcore.ProjectProfile, apiKey, mode, code, continueURL, lang string) string {
	base := s.publicURL
	if u, err := url.Parse(s.publicURL); err == nil && prof.AuthDomain != "" && !strings.EqualFold(prof.AuthDomain, u.Hostname()) {
		base = "https://" + prof.AuthDomain
	}
	if apiKey == "" {
		apiKey = prof.WebAPIKey
	}
	v := url.Values{"mode": {mode}, "oobCode": {code}, "apiKey": {apiKey}, "lang": {lang}}
	if continueURL != "" {
		v.Set("continueUrl", continueURL)
	}
	return base + ActionPath + "?" + v.Encode()
}

// checkContinueURL accepts an empty URL or an absolute http(s) URL on an authorised domain.
func checkContinueURL(cfg queries.AuthProjectConfig, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || len(raw) > 2048 {
		return ErrInvalidContinueURI
	}
	if !authorizedHost(cfg.AuthorizedDomains, u.Hostname()) {
		return ErrUnauthorizedDomain
	}
	return nil
}

// authorizedHost matches a host against the authorised domains the way the web SDK matches an
// origin: a listed domain also covers its subdomains; an IP address matches only itself.
func authorizedHost(domains []string, host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if host == d || (net.ParseIP(host) == nil && net.ParseIP(d) == nil && strings.HasSuffix(host, "."+d)) {
			return true
		}
	}
	return false
}

// lookupCode finds an open code of the project. A code of another project is simply unknown.
func (s *Service) lookupCode(ctx context.Context, p Project, code string) (queries.AuthOobCode, error) {
	if code == "" {
		return queries.AuthOobCode{}, ErrMissingOobCode
	}
	row, err := s.q.GetOobCode(ctx, queries.GetOobCodeParams{ProjectNumber: p.Number, CodeHash: s.oobHash(code)})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return row, ErrInvalidOobCode
	case err != nil:
		return row, err
	case row.UsedAt != nil:
		return row, ErrInvalidOobCode
	case !row.ExpiresAt.After(s.now()):
		return row, ErrExpiredOobCode
	}
	return row, nil
}

// codeUser returns the user a code acts on, refusing a code that no longer fits it: a user
// deleted or recreated, or (except for recovery) an address changed since it was issued.
func (s *Service) codeUser(ctx context.Context, q *queries.Queries, p Project, row queries.AuthOobCode, forUpdate bool) (queries.AuthUser, error) {
	if row.Uid == nil {
		return queries.AuthUser{}, ErrInvalidOobCode
	}
	key := queries.GetUserParams{ProjectNumber: p.Number, TenantID: row.TenantID, Uid: *row.Uid}
	var raw queries.AuthUser
	var err error
	if forUpdate {
		raw, err = q.GetUserForUpdate(ctx, queries.GetUserForUpdateParams(key))
	} else {
		raw, err = q.GetUser(ctx, key)
	}
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return raw, ErrInvalidOobCode
	case err != nil:
		return raw, err
	case raw.CreatedAt.After(row.CreatedAt):
		return raw, ErrInvalidOobCode
	case raw.Disabled:
		return raw, ErrUserDisabled
	case row.RequestType != MailRecoverEmail && !strings.EqualFold(str(raw.Email), row.Email):
		return raw, ErrInvalidOobCode
	case row.RequestType == MailChangeEmail && row.CreatedAt.Before(raw.TokensValidAfter):
		// A change of address is the authority of the session that asked for it, sent to an
		// address that session chose, so it ends with that session: after a password change,
		// a revocation, or takeover protection dropping a squatter's credentials. The other
		// codes go to the account's own address and stay valid.
		return raw, ErrInvalidOobCode
	}
	return raw, nil
}

func codeInfo(cfg queries.AuthProjectConfig, row queries.AuthOobCode) ActionCodeInfo {
	info := ActionCodeInfo{RequestType: row.RequestType, Email: row.Email, NewEmail: str(row.NewEmail)}
	if c := str(row.ContinueUrl); checkContinueURL(cfg, c) == nil {
		info.ContinueURL = c
	}
	return info
}

// CheckActionCode inspects a code without using it: resetPassword without newPassword, which
// verifyPasswordResetCode and checkActionCode call.
func (s *Service) CheckActionCode(ctx context.Context, p Project, code string) (ActionCodeInfo, error) {
	row, err := s.lookupCode(ctx, p, code)
	if err != nil {
		return ActionCodeInfo{}, err
	}
	if _, err := s.codeUser(ctx, s.q, p, row, false); err != nil {
		return ActionCodeInfo{}, err
	}
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return ActionCodeInfo{}, err
	}
	return codeInfo(cfg, row), nil
}

// ResetPassword uses a PASSWORD_RESET code to set a new password. Opening the link proves the
// mailbox, so the email becomes verified (as the emulator does; research §9.9); every session
// of the user is revoked (research §3.8).
func (s *Service) ResetPassword(ctx context.Context, p Project, code, newPassword string) (ActionCodeInfo, error) {
	row, err := s.lookupCode(ctx, p, code)
	if err != nil {
		return ActionCodeInfo{}, err
	}
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return ActionCodeInfo{}, err
	}
	if !cfg.PasswordEnabled {
		return ActionCodeInfo{}, ErrOperationNotAllowed
	}
	if row.RequestType != MailPasswordReset {
		return ActionCodeInfo{}, ErrInvalidOobCode
	}
	if _, err := s.codeUser(ctx, s.q, p, row, false); err != nil {
		return ActionCodeInfo{}, err
	}
	if err := checkPassword(cfg, newPassword); err != nil {
		return ActionCodeInfo{}, err
	}
	hash, err := s.hasher.HashPolicy(ctx, newPassword)
	if err != nil {
		return ActionCodeInfo{}, err
	}
	err = s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		if _, err := q.UseOobCode(ctx, queries.UseOobCodeParams{ProjectNumber: p.Number, CodeHash: row.CodeHash}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalidOobCode
			}
			return err
		}
		raw, err := s.codeUser(ctx, q, p, row, true)
		if err != nil {
			return err
		}
		pr := Project{Number: p.Number, ID: p.ID, Tenant: row.TenantID}
		if _, err := q.UpdateUser(ctx, queries.UpdateUserParams{
			ProjectNumber: p.Number, TenantID: row.TenantID, Uid: raw.Uid, Email: raw.Email, EmailVerified: true,
			DisplayName: raw.DisplayName, PhotoUrl: raw.PhotoUrl, Disabled: raw.Disabled, CustomClaims: raw.CustomClaims,
		}); err != nil {
			return err
		}
		if err := s.attachPassword(ctx, q, pr, raw.Uid, *raw.Email, hash); err != nil {
			return err
		}
		if _, err := q.RevokeUserTokens(ctx, queries.RevokeUserTokensParams{ProjectNumber: p.Number, TenantID: row.TenantID, Uid: raw.Uid}); err != nil {
			return err
		}
		return q.RetireOobCodes(ctx, queries.RetireOobCodesParams{ProjectNumber: p.Number, TenantID: row.TenantID, Uid: &raw.Uid, RequestType: MailPasswordReset})
	})
	if err != nil {
		return ActionCodeInfo{}, err
	}
	s.audit(ctx, s.q, p, *row.Uid, "password.reset", "ok", nil)
	return codeInfo(cfg, row), nil
}

// ApplyActionCode uses a VERIFY_EMAIL, VERIFY_AND_CHANGE_EMAIL or RECOVER_EMAIL code
// (accounts:update with an oobCode). A change of address revokes the user's sessions and sends
// a RECOVER_EMAIL link to the previous address; recovery restores it, verified. lang and apiKey
// shape that recovery link.
func (s *Service) ApplyActionCode(ctx context.Context, p Project, code, lang, apiKey string) (ActionCodeInfo, User, error) {
	row, err := s.lookupCode(ctx, p, code)
	if err != nil {
		return ActionCodeInfo{}, User{}, err
	}
	switch row.RequestType {
	case MailVerifyEmail, MailChangeEmail, MailRecoverEmail:
	default:
		return ActionCodeInfo{}, User{}, ErrInvalidOobCode
	}
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return ActionCodeInfo{}, User{}, err
	}
	prof, err := s.control.ProjectProfile(ctx, p.Number)
	if err != nil {
		return ActionCodeInfo{}, User{}, err
	}
	pr := Project{Number: p.Number, ID: p.ID, Tenant: row.TenantID}
	var u User
	err = s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		if _, err := q.UseOobCode(ctx, queries.UseOobCodeParams{ProjectNumber: p.Number, CodeHash: row.CodeHash}); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalidOobCode
			}
			return err
		}
		raw, err := s.codeUser(ctx, q, pr, row, true)
		if err != nil {
			return err
		}
		upd := queries.UpdateUserParams{
			ProjectNumber: p.Number, TenantID: row.TenantID, Uid: raw.Uid, Email: raw.Email, EmailVerified: true,
			DisplayName: raw.DisplayName, PhotoUrl: raw.PhotoUrl, Disabled: raw.Disabled, CustomClaims: raw.CustomClaims,
		}
		switch row.RequestType {
		case MailChangeEmail:
			upd.Email = row.NewEmail
		case MailRecoverEmail:
			upd.Email = &row.Email
		}
		changed := !strings.EqualFold(str(upd.Email), str(raw.Email))
		updated, err := q.UpdateUser(ctx, upd)
		if err != nil {
			return err
		}
		if changed {
			if _, err := q.GetPassword(ctx, queries.GetPasswordParams{ProjectNumber: p.Number, TenantID: row.TenantID, Uid: raw.Uid}); err == nil {
				if err := q.UpsertProvider(ctx, queries.UpsertProviderParams{ProjectNumber: p.Number, TenantID: row.TenantID, Uid: raw.Uid, ProviderID: "password", RawID: *upd.Email, Email: upd.Email}); err != nil {
					return err
				}
			} else if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if updated.TokensValidAfter, err = q.RevokeUserTokens(ctx, queries.RevokeUserTokensParams{ProjectNumber: p.Number, TenantID: row.TenantID, Uid: raw.Uid}); err != nil {
				return err
			}
		}
		if err := q.RetireOobCodes(ctx, queries.RetireOobCodesParams{ProjectNumber: p.Number, TenantID: row.TenantID, Uid: &raw.Uid, RequestType: row.RequestType}); err != nil {
			return err
		}
		if u, err = s.loadUser(ctx, q, pr, updated); err != nil {
			return err
		}
		if row.RequestType == MailChangeEmail && changed && str(raw.Email) != "" {
			_, err = s.issueCode(ctx, q, pr, cfg, prof, codeRequest{
				kind: MailRecoverEmail, user: u, email: *raw.Email, newEmail: u.Email, to: *raw.Email, lang: mailLanguage(lang), apiKey: apiKey,
			}, true)
		}
		return err
	})
	if _, ok := store.UniqueViolation(err); ok {
		return ActionCodeInfo{}, User{}, ErrEmailExists
	}
	if err != nil {
		return ActionCodeInfo{}, User{}, err
	}
	s.audit(ctx, s.q, p, u.UID, "oob.apply", "ok", map[string]any{"type": row.RequestType})
	return codeInfo(cfg, row), u, nil
}

// The calls below serve the Admin API (generate*Link with returnOobLink) and the console.
// Their caller is authorised for the project, so enumeration protection and the flood limits
// do not apply, and unknown users are reported.

// ActionCodeSettings are the optional settings of an admin-generated link.
type ActionCodeSettings struct {
	ContinueURL string // must be on an authorised domain
	Language    string // "ar" or "en" (a locale such as "en-US" works); empty means Arabic
}

// GeneratePasswordResetLink returns a password reset link for the user with email, as
// generatePasswordResetLink does. Nothing is emailed. Unknown address: EMAIL_NOT_FOUND.
func (s *Service) GeneratePasswordResetLink(ctx context.Context, p Project, email string, settings ActionCodeSettings) (string, error) {
	return s.adminLink(ctx, p, MailPasswordReset, email, "", settings, ErrEmailNotFound)
}

// GenerateEmailVerificationLink returns an email verification link for the user with email,
// as generateEmailVerificationLink does. Nothing is emailed. Unknown address: USER_NOT_FOUND.
func (s *Service) GenerateEmailVerificationLink(ctx context.Context, p Project, email string, settings ActionCodeSettings) (string, error) {
	return s.adminLink(ctx, p, MailVerifyEmail, email, "", settings, ErrUserNotFound)
}

// GenerateVerifyAndChangeEmailLink returns a link that changes the user's email to newEmail
// once opened, as generateVerifyAndChangeEmailLink does. Nothing is emailed. Unknown address:
// USER_NOT_FOUND; newEmail taken: EMAIL_EXISTS.
func (s *Service) GenerateVerifyAndChangeEmailLink(ctx context.Context, p Project, email, newEmail string, settings ActionCodeSettings) (string, error) {
	return s.adminLink(ctx, p, MailChangeEmail, email, newEmail, settings, ErrUserNotFound)
}

func (s *Service) adminLink(ctx context.Context, p Project, kind, email, newEmail string, settings ActionCodeSettings, notFound *Error) (string, error) {
	if email == "" {
		return "", ErrMissingEmail
	}
	addr, ok := normalizeEmail(email)
	if !ok {
		return "", ErrInvalidEmail
	}
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return "", err
	}
	if err := checkContinueURL(cfg, settings.ContinueURL); err != nil {
		return "", err
	}
	raw, err := s.q.GetUserByEmail(ctx, queries.GetUserByEmailParams{ProjectNumber: p.Number, TenantID: p.Tenant, Email: addr})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", notFound
	}
	if err != nil {
		return "", err
	}
	req := codeRequest{kind: kind, email: str(raw.Email), to: str(raw.Email), continueURL: settings.ContinueURL, lang: mailLanguage(settings.Language)}
	if kind == MailChangeEmail {
		if newEmail == "" {
			return "", ErrMissingNewEmail
		}
		if req.newEmail, ok = normalizeEmail(newEmail); !ok {
			return "", ErrInvalidEmail
		}
		if _, err := s.q.GetUserByEmail(ctx, queries.GetUserByEmailParams{ProjectNumber: p.Number, TenantID: p.Tenant, Email: req.newEmail}); err == nil {
			return "", ErrEmailExists
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
		req.to = req.newEmail
	}
	if req.user, err = s.loadUser(ctx, s.q, p, raw); err != nil {
		return "", err
	}
	return s.sendCode(ctx, p, cfg, req, false)
}

// SendPasswordResetEmail emails the user a password reset link and returns the link too, for
// the console's Users screen.
func (s *Service) SendPasswordResetEmail(ctx context.Context, p Project, uid, lang string) (string, error) {
	u, err := s.GetUser(ctx, p, uid)
	if err != nil {
		return "", err
	}
	if u.Email == "" {
		return "", ErrMissingEmail
	}
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return "", err
	}
	return s.sendCode(ctx, p, cfg, codeRequest{kind: MailPasswordReset, user: u, email: u.Email, to: u.Email, lang: mailLanguage(lang)}, true)
}
