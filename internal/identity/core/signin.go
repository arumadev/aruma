package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/events"
	"github.com/osama300/aurma-project/internal/identity/queries"
	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/store"
	tcore "github.com/osama300/aurma-project/internal/tenancy/core"
)

// SignUpInput is accounts:signUp.
type SignUpInput struct {
	Email       string
	Password    string
	DisplayName string
	IDToken     string // link email/password to the current (anonymous) user
}

// SignUp creates an email/password user, an anonymous one (empty input), or upgrades the
// signed-in anonymous user in place, keeping its uid. Validation follows the order Firebase's
// clients observe: missing fields, disabled method, bad email, taken email, weak password.
func (s *Service) SignUp(ctx context.Context, p Project, in SignUpInput) (*Session, error) {
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return nil, err
	}
	anonymous := in.Email == "" && in.Password == "" && in.IDToken == ""
	switch {
	case anonymous:
		if !cfg.AnonymousEnabled {
			return nil, ErrOperationNotAllowed
		}
		if !cfg.ClientSignupEnabled {
			return nil, ErrAdminOnly
		}
		return s.createAndSignIn(ctx, p, "", "", in.DisplayName)
	case in.Email == "":
		return nil, ErrMissingEmail
	case in.Password == "":
		return nil, ErrMissingPassword
	case !cfg.PasswordEnabled:
		return nil, ErrOperationNotAllowed
	}
	email, ok := normalizeEmail(in.Email)
	if !ok {
		return nil, ErrInvalidEmail
	}
	if _, err := s.q.GetUserByEmail(ctx, queries.GetUserByEmailParams{ProjectNumber: p.Number, TenantID: p.Tenant, Email: email}); err == nil {
		return nil, ErrEmailExists // returned even under enumeration protection, as Firebase does
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err := checkPassword(cfg, in.Password); err != nil {
		return nil, err
	}
	if in.IDToken != "" {
		return s.upgradeAnonymous(ctx, p, in.IDToken, email, in.Password, in.DisplayName)
	}
	if !cfg.ClientSignupEnabled {
		return nil, ErrAdminOnly
	}
	return s.createAndSignIn(ctx, p, email, in.Password, in.DisplayName)
}

func (s *Service) createAndSignIn(ctx context.Context, p Project, email, password, displayName string) (*Session, error) {
	if !validDisplayName(displayName) {
		return nil, ErrInvalidDisplayName
	}
	var hash string
	if password != "" {
		var err error
		if hash, err = s.hasher.HashPolicy(ctx, password); err != nil {
			return nil, err
		}
	}
	var sess *Session
	err := s.tx(ctx, func(q *queries.Queries, tx pgx.Tx) error {
		raw, err := q.CreateUser(ctx, queries.CreateUserParams{
			ProjectNumber: p.Number, TenantID: p.Tenant, Uid: newUID(), Email: ptr(email), DisplayName: ptr(displayName),
		})
		if err != nil {
			return err
		}
		if hash != "" {
			if err := s.attachPassword(ctx, q, p, raw.Uid, email, hash); err != nil {
				return err
			}
		}
		u, err := s.loadUser(ctx, q, p, raw)
		if err != nil {
			return err
		}
		if err := s.userEvent(ctx, tx, p, "auth.user.created", u); err != nil {
			return err
		}
		sess, err = s.startSession(ctx, q, p, u, u.SignInProvider(), nil, true)
		return err
	})
	if _, ok := store.UniqueViolation(err); ok {
		return nil, ErrEmailExists
	}
	return sess, err
}

func (s *Service) attachPassword(ctx context.Context, q *queries.Queries, p Project, uid, email, hash string) error {
	if err := q.SetPassword(ctx, queries.SetPasswordParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: uid, Hash: hash}); err != nil {
		return err
	}
	return q.UpsertProvider(ctx, queries.UpsertProviderParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: uid, ProviderID: "password", RawID: email, Email: &email})
}

// upgradeAnonymous turns the signed-in anonymous user into an email/password user, keeping
// its uid (AC-A2).
func (s *Service) upgradeAnonymous(ctx context.Context, p Project, idToken, email, password, displayName string) (*Session, error) {
	u, claims, err := s.VerifyIDToken(ctx, p, idToken)
	if err != nil {
		return nil, err
	}
	// Linking adds a credential; it never replaces one. A new password or a new address for an
	// account that has one is accounts:update's change, with its recent sign-in check, its
	// enumeration rule and its revocation, so a stolen ID token cannot take the account here.
	if u.HasPassword {
		return nil, ErrProviderLinked
	}
	if u.Email != "" && !strings.EqualFold(u.Email, email) {
		cfg, err := s.Config(ctx, p)
		if err != nil {
			return nil, err
		}
		if cfg.EnumerationProtection {
			return nil, failf(ErrOperationNotAllowed.Code, "Please verify the new email before changing email.")
		}
		if err := s.requireRecent(claims); err != nil {
			return nil, err
		}
	}
	hash, err := s.hasher.HashPolicy(ctx, password)
	if err != nil {
		return nil, err
	}
	var sess *Session
	err = s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		raw, err := q.GetUserForUpdate(ctx, queries.GetUserForUpdateParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.UID})
		if err != nil {
			return err
		}
		name := str(raw.DisplayName)
		if displayName != "" {
			name = displayName
		}
		raw, err = q.UpdateUser(ctx, queries.UpdateUserParams{
			ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.UID, Email: &email, EmailVerified: false,
			DisplayName: ptr(name), PhotoUrl: raw.PhotoUrl, Disabled: raw.Disabled, CustomClaims: raw.CustomClaims,
		})
		if err != nil {
			return err
		}
		if err := s.attachPassword(ctx, q, p, u.UID, email, hash); err != nil {
			return err
		}
		upgraded, err := s.loadUser(ctx, q, p, raw)
		if err != nil {
			return err
		}
		sess, err = s.startSession(ctx, q, p, upgraded, "password", nil, false)
		return err
	})
	if _, ok := store.UniqueViolation(err); ok {
		return nil, ErrEmailExists
	}
	return sess, err
}

// SignInWithPassword checks an email and password.
//
// Under enumeration protection (the default) every failed credential check is
// INVALID_LOGIN_CREDENTIALS. A disabled account is reported only after its password has been
// checked: the Firebase emulator says USER_DISABLED first, which tells anyone the account exists
// and is disabled; Aruma takes the defensive reading until production is compared (research
// §3.2).
func (s *Service) SignInWithPassword(ctx context.Context, p Project, email, password string) (*Session, error) {
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return nil, err
	}
	switch {
	case !cfg.PasswordEnabled:
		return nil, ErrPasswordLoginDisabled
	case email == "":
		return nil, ErrMissingEmail
	}
	norm, ok := normalizeEmail(email)
	if !ok {
		return nil, ErrInvalidEmail
	}
	if password == "" {
		return nil, ErrMissingPassword
	}
	notFound, wrong := ErrEmailNotFound, ErrInvalidPassword
	if cfg.EnumerationProtection {
		notFound, wrong = ErrInvalidLogin, ErrInvalidLogin
	}
	raw, err := s.q.GetUserByEmail(ctx, queries.GetUserByEmailParams{ProjectNumber: p.Number, TenantID: p.Tenant, Email: norm})
	if errors.Is(err, pgx.ErrNoRows) {
		s.hasher.VerifyDummy(ctx, password)
		return nil, notFound
	}
	if err != nil {
		return nil, err
	}
	pw, err := s.q.GetPassword(ctx, queries.GetPasswordParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: raw.Uid})
	if errors.Is(err, pgx.ErrNoRows) {
		s.hasher.VerifyDummy(ctx, password)
		return nil, wrong
	}
	if err != nil {
		return nil, err
	}
	ok, rehash, err := s.hasher.Verify(ctx, password, pw.Hash)
	if err != nil {
		return nil, err
	}
	if !ok {
		s.audit(ctx, s.q, p, raw.Uid, "signin.password", "denied", nil)
		return nil, wrong
	}
	// Only someone who knows the password learns that the account is disabled, so protection
	// does not hide it: the documented protection replaces EMAIL_NOT_FOUND and INVALID_PASSWORD
	// only, and the web SDK needs USER_DISABLED to show auth/user-disabled.
	if raw.Disabled {
		return nil, ErrUserDisabled
	}
	if rehash {
		if h, err := s.hasher.HashPolicy(ctx, password); err == nil {
			_ = s.q.SetPassword(ctx, queries.SetPasswordParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: raw.Uid, Hash: h})
		}
	}
	var sess *Session
	err = s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		u, err := s.loadUser(ctx, q, p, raw)
		if err != nil {
			return err
		}
		sess, err = s.startSession(ctx, q, p, u, "password", nil, false)
		return err
	})
	if err == nil {
		s.audit(ctx, s.q, p, raw.Uid, "signin.password", "ok", nil)
	}
	return sess, err
}

// customTokenAudience is the audience createCustomToken writes; Aruma accepts the official
// tokens unchanged (ADR-004 §5).
const customTokenAudience = "https://identitytoolkit.googleapis.com/google.identity.identitytoolkit.v1.IdentityToolkit"

type customTokenClaims struct {
	Iss    string          `json:"iss"`
	Sub    string          `json:"sub"`
	Aud    string          `json:"aud"`
	Iat    int64           `json:"iat"`
	Exp    int64           `json:"exp"`
	UID    string          `json:"uid"`
	Claims json.RawMessage `json:"claims"`
}

// SignInWithCustomToken accepts a token from the official createCustomToken, signed by a
// service account of the same project.
func (s *Service) SignInWithCustomToken(ctx context.Context, p Project, token string) (*Session, error) {
	if token == "" {
		return nil, ErrMissingCustomToken
	}
	a, err := s.control.VerifyServiceAccountJWT(ctx, p.Number, token)
	if err != nil {
		if errors.Is(err, tcore.ErrOtherProject) || apierr.CodeOf(err) == apierr.PermissionDenied {
			return nil, ErrCredentialMismatch
		}
		return nil, ErrInvalidCustomToken
	}
	var c customTokenClaims
	if json.Unmarshal(a.Payload, &c) != nil || c.Aud != customTokenAudience || c.Iss != a.Email || c.Sub != a.Email {
		return nil, ErrInvalidCustomToken
	}
	now := s.now().Unix()
	if c.Exp <= now || c.Exp > c.Iat+3600 || c.Iat > now+300 {
		return nil, ErrInvalidCustomToken
	}
	if c.UID == "" || len(c.UID) > 128 {
		return nil, ErrInvalidCustomToken
	}
	claims, err := validClaims(c.Claims)
	if err != nil {
		return nil, err
	}
	var sess *Session
	err = s.tx(ctx, func(q *queries.Queries, tx pgx.Tx) error {
		raw, err := q.GetUser(ctx, queries.GetUserParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: c.UID})
		isNew := false
		if errors.Is(err, pgx.ErrNoRows) {
			isNew = true
			raw, err = q.CreateUser(ctx, queries.CreateUserParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: c.UID})
		}
		if err != nil {
			return err
		}
		if raw.Disabled {
			return ErrUserDisabled
		}
		u, err := s.loadUser(ctx, q, p, raw)
		if err != nil {
			return err
		}
		if isNew {
			if err := s.userEvent(ctx, tx, p, "auth.user.created", u); err != nil {
				return err
			}
		}
		sess, err = s.startSession(ctx, q, p, u, "custom", claims, isNew)
		return err
	})
	return sess, err
}

// userEvent appends an Auth change to the shared log, in the caller's transaction (AC-A16).
func (s *Service) userEvent(ctx context.Context, tx pgx.Tx, p Project, typ string, u User) error {
	data, _ := json.Marshal(map[string]any{
		"uid": u.UID, "email": u.Email, "emailVerified": u.EmailVerified, "displayName": u.DisplayName,
		"photoURL": u.PhotoURL, "disabled": u.Disabled, "providers": providerIDs(u), "tenantId": p.Tenant,
		"createdAt": u.CreatedAt.UTC().Format(time.RFC3339),
	})
	_, err := events.Append(ctx, tx, events.Change{ProjectNumber: p.Number, Service: "auth", Type: typ, Subject: u.UID, Data: data})
	return err
}

func providerIDs(u User) []string {
	out := make([]string, 0, len(u.Providers))
	for _, pr := range u.Providers {
		out = append(out, pr.ProviderID)
	}
	return out
}
