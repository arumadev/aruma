package core

import (
	"context"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/identity/queries"
	"github.com/osama300/aurma-project/internal/store"
)

// Lookup returns the signed-in user (accounts:lookup). The client SDKs call it right after
// every sign-in, so it accepts a token issued a moment earlier.
func (s *Service) Lookup(ctx context.Context, p Project, idToken string) (User, error) {
	u, _, err := s.VerifyIDToken(ctx, p, idToken)
	return u, err
}

// UpdateInput is the client form of accounts:update. Nil fields are left unchanged.
type UpdateInput struct {
	IDToken         string
	Email           *string
	Password        *string
	DisplayName     *string
	PhotoURL        *string
	DeleteAttribute []string // DISPLAY_NAME, PHOTO_URL
	DeleteProvider  []string
}

// UpdateResult is the updated user, and a new session when the email or password changed:
// those changes revoke the user's other sessions.
type UpdateResult struct {
	User    User
	Session *Session
}

// Update applies a profile, email, password or unlink change for the signed-in user.
func (s *Service) Update(ctx context.Context, p Project, in UpdateInput) (*UpdateResult, error) {
	u, claims, err := s.VerifyIDToken(ctx, p, in.IDToken)
	if err != nil {
		return nil, err
	}
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return nil, err
	}
	sensitive := in.Email != nil || in.Password != nil
	if sensitive {
		if err := s.requireRecent(claims); err != nil {
			return nil, err
		}
	}
	var email string
	if in.Email != nil {
		if cfg.EnumerationProtection {
			// Changing the email without verifying it would reveal which addresses are taken.
			return nil, failf(ErrOperationNotAllowed.Code, "Please verify the new email before changing email.")
		}
		var ok bool
		if email, ok = normalizeEmail(*in.Email); !ok {
			return nil, ErrInvalidEmail
		}
	}
	var hash string
	if in.Password != nil {
		if err := checkPassword(cfg, *in.Password); err != nil {
			return nil, err
		}
		if hash, err = s.hasher.HashPolicy(ctx, *in.Password); err != nil {
			return nil, err
		}
	}
	name, photo := u.DisplayName, u.PhotoURL
	if in.DisplayName != nil {
		if !validDisplayName(*in.DisplayName) {
			return nil, ErrInvalidDisplayName
		}
		name = *in.DisplayName
	}
	if in.PhotoURL != nil {
		if !validPhotoURL(*in.PhotoURL) {
			return nil, ErrInvalidPhotoURL
		}
		photo = *in.PhotoURL
	}
	for _, a := range in.DeleteAttribute {
		switch a {
		case "DISPLAY_NAME":
			name = ""
		case "PHOTO_URL":
			photo = ""
		default:
			return nil, failf("INVALID_REQ_TYPE", "unsupported deleteAttribute "+a)
		}
	}

	var out UpdateResult
	err = s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		raw, err := q.GetUserForUpdate(ctx, queries.GetUserForUpdateParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.UID})
		if err != nil {
			return err
		}
		newEmail, verified := raw.Email, raw.EmailVerified
		if in.Email != nil && email != str(raw.Email) {
			newEmail, verified = &email, false
		}
		raw, err = q.UpdateUser(ctx, queries.UpdateUserParams{
			ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.UID, Email: newEmail, EmailVerified: verified,
			DisplayName: ptr(name), PhotoUrl: ptr(photo), Disabled: raw.Disabled, CustomClaims: raw.CustomClaims,
		})
		if err != nil {
			return err
		}
		if len(in.DeleteProvider) > 0 {
			if err := q.DeleteProviders(ctx, queries.DeleteProvidersParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.UID, ProviderIds: in.DeleteProvider}); err != nil {
				return err
			}
			if slices.Contains(in.DeleteProvider, "password") {
				if err := q.DeletePassword(ctx, queries.DeletePasswordParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.UID}); err != nil {
					return err
				}
			}
		}
		if hash != "" {
			if raw.Email == nil {
				return ErrMissingEmail // a password needs an email to sign in with
			}
			if err := s.attachPassword(ctx, q, p, u.UID, *raw.Email, hash); err != nil {
				return err
			}
		} else if in.Email != nil && u.HasPassword {
			if err := q.UpsertProvider(ctx, queries.UpsertProviderParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.UID, ProviderID: "password", RawID: email, Email: &email}); err != nil {
				return err
			}
		}
		if sensitive {
			if raw.TokensValidAfter, err = q.RevokeUserTokens(ctx, queries.RevokeUserTokensParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.UID}); err != nil {
				return err
			}
		}
		if out.User, err = s.loadUser(ctx, q, p, raw); err != nil {
			return err
		}
		if sensitive {
			provider := claims.Firebase.SignInProvider
			if hash != "" {
				provider = "password"
			}
			out.Session, err = s.startSession(ctx, q, p, out.User, provider, nil, false)
		}
		return err
	})
	if _, ok := store.UniqueViolation(err); ok {
		return nil, ErrEmailExists
	}
	if err != nil {
		return nil, err
	}
	if sensitive {
		s.audit(ctx, s.q, p, u.UID, "account.update", "ok", map[string]any{"email": in.Email != nil, "password": in.Password != nil})
	}
	return &out, nil
}

// Delete removes the signed-in user (accounts:delete), which needs a recent sign-in.
func (s *Service) Delete(ctx context.Context, p Project, idToken string) error {
	u, claims, err := s.VerifyIDToken(ctx, p, idToken)
	if err != nil {
		return err
	}
	if err := s.requireRecent(claims); err != nil {
		return err
	}
	return s.deleteUser(ctx, p, u)
}

// deleteUser removes a user and records auth.user.deleted in the same transaction. Its refresh
// tokens stay behind so that they answer USER_NOT_FOUND, which signs the clients out.
func (s *Service) deleteUser(ctx context.Context, p Project, u User) error {
	appleToken := s.appleRefreshToken(ctx, s.q, p, u.UID)
	err := s.tx(ctx, func(q *queries.Queries, tx pgx.Tx) error {
		n, err := q.DeleteUser(ctx, queries.DeleteUserParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.UID})
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrUserNotFound
		}
		return s.userEvent(ctx, tx, p, "auth.user.deleted", u)
	})
	if err == nil {
		s.audit(ctx, s.q, p, u.UID, "account.delete", "ok", nil)
		s.revokeAppleOnDelete(ctx, p, u.UID, appleToken)
	}
	return err
}

// SignInMethods answers accounts:createAuthUri for an email. Under enumeration protection the
// list is always empty and registered is not reported.
func (s *Service) SignInMethods(ctx context.Context, p Project, identifier string) (methods []string, registered *bool, err error) {
	if identifier == "" {
		return nil, nil, failf("MISSING_IDENTIFIER", "")
	}
	email, ok := normalizeEmail(identifier)
	if !ok {
		return nil, nil, failf("INVALID_IDENTIFIER", "")
	}
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return nil, nil, err
	}
	if cfg.EnumerationProtection {
		return nil, nil, nil
	}
	raw, err := s.q.GetUserByEmail(ctx, queries.GetUserByEmailParams{ProjectNumber: p.Number, TenantID: p.Tenant, Email: email})
	if errors.Is(err, pgx.ErrNoRows) {
		no := false
		return nil, &no, nil
	}
	if err != nil {
		return nil, nil, err
	}
	u, err := s.loadUser(ctx, s.q, p, raw)
	if err != nil {
		return nil, nil, err
	}
	yes := true
	return providerIDs(u), &yes, nil
}
