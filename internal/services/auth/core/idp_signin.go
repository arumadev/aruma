package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/services/auth/queries"
	"github.com/osama300/aurma-project/internal/store"
)

// IdPInput is accounts:signInWithIdp: the provider's answer as the SDK relays it. A redirect
// flow names its session by the state in requestUri or postBody and proves it with
// sessionId; a direct credential carries the provider's id_token in postBody.
type IdPInput struct {
	RequestURI   string
	PostBody     string
	SessionID    string
	IDToken      string // the signed-in user, to link the provider to
	PendingToken string
	AutoCreate   *bool
}

// IdPResult is the provider's view of the user and, unless the account needs confirmation,
// a session.
type IdPResult struct {
	ProviderID       string
	RawID            string
	FederatedID      string
	Email            string
	EmailVerified    bool
	FirstName        string
	LastName         string
	DisplayName      string
	PhotoURL         string
	RawUserInfo      string
	OAuthIDToken     string
	OAuthAccessToken string
	OAuthExpiresIn   int

	// NeedConfirmation: an account already uses the email, and the provider does not vouch
	// for it, so the user must sign in to that account first and link (Firebase's
	// account-exists-with-different-credential). VerifiedProviders lists its providers,
	// except under enumeration protection.
	NeedConfirmation  bool
	VerifiedProviders []string

	Session *Session
}

// Errors of signInWithIdp; the SDKs map them to auth/<code>.
var (
	ErrInvalidNonce         = failf("MISSING_OR_INVALID_NONCE", "the id_token nonce does not match")
	ErrFederatedLinked      = fail("FEDERATED_USER_ID_ALREADY_LINKED")
	ErrProviderLinked       = fail("PROVIDER_ALREADY_LINKED")
	ErrUserCancelled        = fail("USER_CANCELLED")
	errMissingRequestURI    = fail("MISSING_REQUEST_URI")
	errInvalidPendingToken  = failf("INVALID_PENDING_TOKEN", "Aruma does not issue pending tokens")
	errInvalidProviderInput = failf("INVALID_CREDENTIAL_OR_PROVIDER_ID", "postBody needs providerId and id_token")
)

// CredentialError reports whether err leaves a credential worth returning: the SDKs turn
// these into errors that carry the provider credential (credentialFromError), so the app can
// sign in to the other account and link.
func CredentialError(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Code == ErrFederatedLinked.Code || e.Code == ErrEmailExists.Code)
}

// assertion is a verified provider answer.
type assertion struct {
	idp      *idpCreds
	claims   *providerClaims
	tokens   providerTokens
	authType string
	user     appleUser
}

// appleUser is the name Apple posts once, on the first consent. It is not signed, so only the
// name is taken from it, never the email.
type appleUser struct {
	Name struct {
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
	} `json:"name"`
}

// SignInWithIdP signs in with a provider, creating, linking or confirming accounts under one
// account per email (research: docs/research/phase2-idp.md §3).
func (s *Service) SignInWithIdP(ctx context.Context, p Project, in IdPInput) (*IdPResult, error) {
	switch {
	case in.PendingToken != "":
		return nil, errInvalidPendingToken
	case in.RequestURI == "":
		return nil, errMissingRequestURI
	}
	a, err := s.idpAssertion(ctx, p, in)
	if err != nil {
		s.audit(ctx, s.q, p, "", "signin.idp", "denied", map[string]any{"error": err.Error()})
		return nil, err
	}
	r := profile(a)
	var linkTo *User
	if in.IDToken != "" {
		u, _, err := s.VerifyIDToken(ctx, p, in.IDToken)
		if err != nil {
			return nil, err
		}
		linkTo = &u
	}
	autoCreate := (in.AutoCreate == nil || *in.AutoCreate) && !strings.HasPrefix(a.authType, "reauth")
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return nil, err
	}
	err = s.tx(ctx, func(q *queries.Queries, tx pgx.Tx) error {
		if linkTo != nil {
			return s.linkIdP(ctx, q, p, a, r, *linkTo)
		}
		return s.signInIdP(ctx, q, tx, p, cfg, a, r, autoCreate)
	})
	if c, ok := store.UniqueViolation(err); ok {
		err = ErrEmailExists
		if strings.Contains(c, "provider") {
			err = ErrFederatedLinked
		}
	}
	if err != nil {
		if CredentialError(err) {
			return r, err
		}
		return nil, err
	}
	if r.Session != nil {
		s.audit(ctx, s.q, p, r.Session.User.UID, "signin.idp", "ok", map[string]any{"provider": r.ProviderID, "link": linkTo != nil})
	}
	return r, nil
}

// idpAssertion verifies the provider's answer. In a redirect flow the requestUri must be the
// callback the session was started with; a direct credential needs no redirect, so its
// requestUri (the SDK sends http://localhost) is not a trust decision.
func (s *Service) idpAssertion(ctx context.Context, p Project, in IdPInput) (*assertion, error) {
	ru, err := url.Parse(in.RequestURI)
	if err != nil {
		return nil, failf("INVALID_REQUEST_URI", "")
	}
	post, err := url.ParseQuery(in.PostBody)
	if err != nil {
		return nil, errIdPToken("postBody is not form-encoded")
	}
	get := func(k string) string {
		if v := post.Get(k); v != "" {
			return v
		}
		return ru.Query().Get(k)
	}
	a := &assertion{}
	if raw := post.Get("user"); raw != "" {
		_ = json.Unmarshal([]byte(raw), &a.user)
	}
	state := get("state")
	if state == "" {
		provider, token := post.Get("providerId"), post.Get("id_token")
		if provider == "" {
			return nil, errInvalidProviderInput
		}
		if a.idp, err = s.enabledIdP(ctx, p, provider); err != nil {
			return nil, err
		}
		if token == "" {
			return nil, errIdPToken("Aruma signs in with the provider's id_token; pass id_token, not only an access token")
		}
		if a.claims, err = s.verifyProviderToken(ctx, a.idp, token); err != nil {
			return nil, err
		}
		raw := post.Get("nonce")
		if (raw != "" || a.claims.Nonce != "") && (raw == "" || a.claims.Nonce != hashNonce(raw)) {
			return nil, ErrInvalidNonce
		}
		a.tokens.IDToken = token
		a.tokens.AccessToken = post.Get("access_token")
		return a, nil
	}

	row, err := s.redeemIdP(ctx, p, state, in.SessionID)
	if err != nil {
		return nil, err
	}
	cb, _ := url.Parse(row.CallbackUrl)
	if cb == nil || ru.Scheme != cb.Scheme || ru.Host != cb.Host || ru.Path != cb.Path {
		return nil, ErrUnauthorizedDomain
	}
	if pid := post.Get("providerId"); pid != "" && pid != row.ProviderID {
		return nil, errInvalidProviderInput
	}
	if a.idp, err = s.enabledIdP(ctx, p, row.ProviderID); err != nil {
		return nil, err
	}
	a.authType = row.AuthType
	if e := get("error"); e != "" {
		if e == "access_denied" || e == "user_cancelled_authorize" {
			return nil, ErrUserCancelled
		}
		return nil, errIdPToken("the provider refused the sign-in")
	}
	switch code, token := get("code"), post.Get("id_token"); {
	case code != "":
		t, err := s.exchangeCode(ctx, p, a.idp, row, code)
		if err != nil {
			return nil, err
		}
		a.tokens = *t
	case token != "":
		a.tokens.IDToken = token
	default:
		return nil, errIdPToken("the provider returned neither a code nor an id_token")
	}
	if a.claims, err = s.verifyProviderToken(ctx, a.idp, a.tokens.IDToken); err != nil {
		return nil, err
	}
	if a.claims.Nonce != hashNonce(row.Nonce) {
		return nil, ErrInvalidNonce
	}
	return a, nil
}

// profile is the provider's view of the user, in signInWithIdp's response fields.
func profile(a *assertion) *IdPResult {
	c := a.claims
	r := &IdPResult{
		ProviderID: a.idp.provider, RawID: c.Sub, FederatedID: c.Sub,
		OAuthIDToken: a.tokens.IDToken, OAuthAccessToken: a.tokens.AccessToken, OAuthExpiresIn: a.tokens.ExpiresIn,
	}
	if email, ok := normalizeEmail(c.Email); ok {
		r.Email, r.EmailVerified = email, bool(c.EmailVerified)
	}
	switch a.idp.provider {
	case GoogleProvider:
		r.FederatedID = "https://accounts.google.com/" + c.Sub
		r.DisplayName, r.FirstName, r.LastName = c.Name, c.GivenName, c.FamilyName
		if validPhotoURL(c.Picture) {
			r.PhotoURL = c.Picture
		}
		info := map[string]any{"id": c.Sub, "verified_email": r.EmailVerified}
		for k, v := range map[string]string{"email": r.Email, "name": c.Name, "given_name": c.GivenName, "family_name": c.FamilyName, "picture": r.PhotoURL, "granted_scopes": a.tokens.Scope} {
			if v != "" {
				info[k] = v
			}
		}
		b, _ := json.Marshal(info)
		r.RawUserInfo = string(b)
	case AppleProvider:
		r.FirstName, r.LastName = a.user.Name.FirstName, a.user.Name.LastName
		r.DisplayName = strings.TrimSpace(r.FirstName + " " + r.LastName)
		r.RawUserInfo = string(c.raw)
	}
	if !validDisplayName(r.DisplayName) {
		r.DisplayName = ""
	}
	return r
}

// signInIdP resolves the account for a sign-in: the one linked to this provider identity;
// else, when the provider vouches for the email, the account with that email (whose password
// and other providers are dropped if its email was never verified, so whoever registered the
// address first cannot keep a way in); else a confirmation is needed; else a new account.
func (s *Service) signInIdP(ctx context.Context, q *queries.Queries, tx pgx.Tx, p Project, cfg queries.AuthProjectConfig, a *assertion, r *IdPResult, autoCreate bool) error {
	raw, err := q.GetUserByProvider(ctx, queries.GetUserByProviderParams{ProjectNumber: p.Number, TenantID: p.Tenant, ProviderID: r.ProviderID, RawID: r.RawID})
	switch {
	case err == nil:
		return s.finishIdP(ctx, q, p, a, r, raw, false)
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	if r.Email != "" {
		raw, err := q.GetUserByEmail(ctx, queries.GetUserByEmailParams{ProjectNumber: p.Number, TenantID: p.Tenant, Email: r.Email})
		switch {
		case err == nil && r.EmailVerified:
			if raw.Disabled {
				return ErrUserDisabled
			}
			name, photo := str(raw.DisplayName), str(raw.PhotoUrl)
			if !raw.EmailVerified {
				if err := s.dropCredentials(ctx, q, p, raw.Uid); err != nil {
					return err
				}
				name, photo = r.DisplayName, r.PhotoURL
				s.audit(ctx, q, p, raw.Uid, "signin.idp.takeover_protection", "ok", map[string]any{"provider": r.ProviderID})
			}
			if raw, err = q.UpdateUser(ctx, queries.UpdateUserParams{
				ProjectNumber: p.Number, TenantID: p.Tenant, Uid: raw.Uid, Email: raw.Email, EmailVerified: true,
				DisplayName: ptr(firstNonEmpty(name, r.DisplayName)), PhotoUrl: ptr(firstNonEmpty(photo, r.PhotoURL)),
				Disabled: raw.Disabled, CustomClaims: raw.CustomClaims,
			}); err != nil {
				return err
			}
			return s.finishIdP(ctx, q, p, a, r, raw, false)
		case err == nil:
			r.NeedConfirmation = true
			if !cfg.EnumerationProtection {
				u, err := s.loadUser(ctx, q, p, raw)
				if err != nil {
					return err
				}
				for _, id := range providerIDs(u) {
					if id != "password" {
						r.VerifiedProviders = append(r.VerifiedProviders, id)
					}
				}
			}
			return nil
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
	}
	switch {
	case !autoCreate:
		return ErrUserNotFound
	case !cfg.ClientSignupEnabled:
		return ErrAdminOnly
	}
	raw, err = q.CreateUser(ctx, queries.CreateUserParams{
		ProjectNumber: p.Number, TenantID: p.Tenant, Uid: newUID(), Email: ptr(r.Email), EmailVerified: r.EmailVerified,
		DisplayName: ptr(r.DisplayName), PhotoUrl: ptr(r.PhotoURL),
	})
	if err != nil {
		return err
	}
	if err := s.finishIdP(ctx, q, p, a, r, raw, true); err != nil {
		return err
	}
	return s.userEvent(ctx, tx, p, "auth.user.created", r.Session.User)
}

// dropCredentials removes an account's password and providers and ends its sessions.
func (s *Service) dropCredentials(ctx context.Context, q *queries.Queries, p Project, uid string) error {
	if err := q.DeletePassword(ctx, queries.DeletePasswordParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: uid}); err != nil {
		return err
	}
	if err := q.DeleteProviders(ctx, queries.DeleteProvidersParams{
		ProjectNumber: p.Number, TenantID: p.Tenant, Uid: uid, ProviderIds: []string{"password", GoogleProvider, AppleProvider},
	}); err != nil {
		return err
	}
	_, err := q.RevokeUserTokens(ctx, queries.RevokeUserTokensParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: uid})
	return err
}

// linkIdP adds the provider to the signed-in user, keeping its uid (an anonymous user is
// upgraded in place). The identity must not belong to another account, nor its email.
func (s *Service) linkIdP(ctx context.Context, q *queries.Queries, p Project, a *assertion, r *IdPResult, u User) error {
	owner, err := q.GetUserByProvider(ctx, queries.GetUserByProviderParams{ProjectNumber: p.Number, TenantID: p.Tenant, ProviderID: r.ProviderID, RawID: r.RawID})
	switch {
	case err == nil && owner.Uid != u.UID:
		return ErrFederatedLinked
	case err != nil && !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	for _, pr := range u.Providers {
		if pr.ProviderID == r.ProviderID && pr.RawID != r.RawID {
			return ErrProviderLinked
		}
	}
	if r.Email != "" {
		other, err := q.GetUserByEmail(ctx, queries.GetUserByEmailParams{ProjectNumber: p.Number, TenantID: p.Tenant, Email: r.Email})
		switch {
		case err == nil && other.Uid != u.UID:
			return ErrEmailExists
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return err
		}
	}
	raw, err := q.GetUserForUpdate(ctx, queries.GetUserForUpdateParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.UID})
	if err != nil {
		return err
	}
	email, verified := raw.Email, raw.EmailVerified
	switch {
	case raw.Email == nil && r.Email != "":
		email, verified = &r.Email, r.EmailVerified
	case raw.Email != nil && strings.EqualFold(*raw.Email, r.Email) && r.EmailVerified:
		verified = true
	}
	if raw, err = q.UpdateUser(ctx, queries.UpdateUserParams{
		ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.UID, Email: email, EmailVerified: verified,
		DisplayName: ptr(firstNonEmpty(str(raw.DisplayName), r.DisplayName)), PhotoUrl: ptr(firstNonEmpty(str(raw.PhotoUrl), r.PhotoURL)),
		Disabled: raw.Disabled, CustomClaims: raw.CustomClaims,
	}); err != nil {
		return err
	}
	return s.finishIdP(ctx, q, p, a, r, raw, false)
}

// finishIdP records the provider on the account, keeps Apple's refresh token for revocation,
// and starts the session.
func (s *Service) finishIdP(ctx context.Context, q *queries.Queries, p Project, a *assertion, r *IdPResult, raw queries.AuthUser, isNew bool) error {
	if raw.Disabled {
		return ErrUserDisabled
	}
	if err := q.UpsertProvider(ctx, queries.UpsertProviderParams{
		ProjectNumber: p.Number, TenantID: p.Tenant, Uid: raw.Uid, ProviderID: r.ProviderID, RawID: r.RawID,
		Email: ptr(r.Email), DisplayName: ptr(r.DisplayName), PhotoUrl: ptr(r.PhotoURL),
	}); err != nil {
		return err
	}
	if a.idp.provider == AppleProvider && a.tokens.RefreshToken != "" {
		enc, err := s.keys.Seal([]byte(a.tokens.RefreshToken), idpTokenAAD(p, raw.Uid, AppleProvider))
		if err != nil {
			return err
		}
		if err := q.UpsertIdPToken(ctx, queries.UpsertIdPTokenParams{
			ProjectNumber: p.Number, TenantID: p.Tenant, Uid: raw.Uid, ProviderID: AppleProvider, RefreshTokenEnc: enc,
		}); err != nil {
			return err
		}
	}
	u, err := s.loadUser(ctx, q, p, raw)
	if err != nil {
		return err
	}
	r.Session, err = s.startSession(ctx, q, p, u, r.ProviderID, nil, isNew)
	return err
}

func idpTokenAAD(p Project, uid, provider string) []byte {
	return []byte("auth.idp_tokens|refresh_token_enc|" + strconv.FormatInt(p.Number, 10) + "/" + p.Tenant + "/" + uid + "/" + provider)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
