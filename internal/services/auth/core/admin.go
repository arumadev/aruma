package core

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/services/auth/queries"
	"github.com/osama300/aurma-project/internal/store"
)

// The operations below serve the Admin API and the console. Their callers have already
// authorised the request for the project, so they skip the client policy (sign-up switches,
// enumeration protection, recent sign-in) and act on any user by uid.

// maxBatch bounds lookups and deletions in one call, as Identity Toolkit does.
const maxBatch = 1000

// AdminUser is the form of createUser and the fields updateUser may set. Nil fields are left
// unchanged on update.
type AdminUser struct {
	UID             string
	Email           *string
	Password        *string
	DisplayName     *string
	PhotoURL        *string
	EmailVerified   *bool
	Disabled        *bool
	CustomClaims    *json.RawMessage // empty, null or {} clears them
	DeleteProviders []string
	RevokeTokens    bool // move tokens_valid_after to now (revokeRefreshTokens)
}

// GetUsers returns the users matching any of the uids or emails; unknown ones are skipped.
func (s *Service) GetUsers(ctx context.Context, p Project, uids, emails []string) ([]User, error) {
	if len(uids)+len(emails) > maxBatch {
		return nil, failf("TOO_MANY_IDENTIFIERS", "at most 1000 identifiers")
	}
	var rows []queries.AuthUser
	if len(uids) > 0 {
		r, err := s.q.GetUsersByUIDs(ctx, queries.GetUsersByUIDsParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uids: uids})
		if err != nil {
			return nil, err
		}
		rows = append(rows, r...)
	}
	if len(emails) > 0 {
		lower := make([]string, len(emails))
		for i, e := range emails {
			lower[i] = strings.ToLower(strings.TrimSpace(e))
		}
		r, err := s.q.GetUsersByEmails(ctx, queries.GetUsersByEmailsParams{ProjectNumber: p.Number, TenantID: p.Tenant, Emails: lower})
		if err != nil {
			return nil, err
		}
		for _, u := range r {
			if !slices.ContainsFunc(rows, func(x queries.AuthUser) bool { return x.Uid == u.Uid }) {
				rows = append(rows, u)
			}
		}
	}
	return s.loadUsers(ctx, p, rows)
}

// GetUser returns one user by uid.
func (s *Service) GetUser(ctx context.Context, p Project, uid string) (User, error) {
	raw, err := s.q.GetUser(ctx, queries.GetUserParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUserNotFound
	}
	if err != nil {
		return User{}, err
	}
	return s.loadUser(ctx, s.q, p, raw)
}

// ListUsers returns a page of users ordered by uid. The page token is the last uid of the
// previous page; an empty next token means there are no more.
func (s *Service) ListUsers(ctx context.Context, p Project, pageToken string, max int) ([]User, string, error) {
	if max <= 0 || max > maxBatch {
		max = maxBatch
	}
	rows, err := s.q.ListUsers(ctx, queries.ListUsersParams{ProjectNumber: p.Number, TenantID: p.Tenant, After: pageToken, PageSize: int32(max + 1)})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > max {
		rows = rows[:max]
		next = rows[max-1].Uid
	}
	users, err := s.loadUsers(ctx, p, rows)
	return users, next, err
}

// CountUsers returns the number of users in the project.
func (s *Service) CountUsers(ctx context.Context, p Project) (int64, error) {
	return s.q.CountUsers(ctx, queries.CountUsersParams{ProjectNumber: p.Number, TenantID: p.Tenant})
}

// loadUsers attaches providers and password state to many users in two queries.
func (s *Service) loadUsers(ctx context.Context, p Project, rows []queries.AuthUser) ([]User, error) {
	if len(rows) == 0 {
		return []User{}, nil
	}
	uids := make([]string, len(rows))
	for i, r := range rows {
		uids[i] = r.Uid
	}
	providers, err := s.q.ListProvidersForUsers(ctx, queries.ListProvidersForUsersParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uids: uids})
	if err != nil {
		return nil, err
	}
	withPassword, err := s.q.PasswordUsersExist(ctx, queries.PasswordUsersExistParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uids: uids})
	if err != nil {
		return nil, err
	}
	out := make([]User, len(rows))
	for i, r := range rows {
		out[i] = toUser(r)
		out[i].HasPassword = slices.Contains(withPassword, r.Uid)
		for _, pr := range providers {
			if pr.Uid == r.Uid {
				out[i].Providers = append(out[i].Providers, Provider{ProviderID: pr.ProviderID, RawID: pr.RawID, Email: str(pr.Email), DisplayName: str(pr.DisplayName), PhotoURL: str(pr.PhotoUrl)})
			}
		}
	}
	return out, nil
}

// validUID accepts the uids Firebase does: 1 to 128 characters.
func validUID(uid string) bool { return uid != "" && len(uid) <= 128 }

// CreateUser creates a user as createUser does. The password, when given, follows the
// project's policy; an empty uid gets a generated one.
func (s *Service) CreateUser(ctx context.Context, p Project, in AdminUser) (User, error) {
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return User{}, err
	}
	uid := in.UID
	if uid == "" {
		uid = newUID()
	} else if !validUID(uid) {
		return User{}, ErrInvalidUID
	}
	var email string
	if in.Email != nil {
		var ok bool
		if email, ok = normalizeEmail(*in.Email); !ok {
			return User{}, ErrInvalidEmail
		}
	}
	name, photo := deref(in.DisplayName), deref(in.PhotoURL)
	if !validDisplayName(name) {
		return User{}, ErrInvalidDisplayName
	}
	if !validPhotoURL(photo) {
		return User{}, ErrInvalidPhotoURL
	}
	var hash string
	if in.Password != nil {
		if email == "" {
			return User{}, ErrMissingEmail
		}
		if err := checkPassword(cfg, *in.Password); err != nil {
			return User{}, err
		}
		if hash, err = s.hasher.HashPolicy(ctx, *in.Password); err != nil {
			return User{}, err
		}
	}
	var claims json.RawMessage
	if in.CustomClaims != nil {
		if claims, err = validClaims(*in.CustomClaims); err != nil {
			return User{}, err
		}
	}
	var u User
	err = s.tx(ctx, func(q *queries.Queries, tx pgx.Tx) error {
		raw, err := q.CreateUser(ctx, queries.CreateUserParams{
			ProjectNumber: p.Number, TenantID: p.Tenant, Uid: uid, Email: ptr(email), EmailVerified: derefBool(in.EmailVerified),
			DisplayName: ptr(name), PhotoUrl: ptr(photo), Disabled: derefBool(in.Disabled), CustomClaims: claims,
		})
		if err != nil {
			return err
		}
		if hash != "" {
			if err := s.attachPassword(ctx, q, p, uid, email, hash); err != nil {
				return err
			}
		}
		if u, err = s.loadUser(ctx, q, p, raw); err != nil {
			return err
		}
		return s.userEvent(ctx, tx, p, "auth.user.created", u)
	})
	if c, ok := store.UniqueViolation(err); ok {
		if strings.Contains(c, "email") {
			return User{}, ErrEmailExists
		}
		return User{}, ErrUIDExists
	}
	if err == nil {
		s.audit(ctx, s.q, p, uid, "admin.user.create", "ok", nil)
	}
	return u, err
}

// UpdateUser applies an admin change. A new email or password revokes the user's sessions,
// as does RevokeTokens.
func (s *Service) UpdateUser(ctx context.Context, p Project, in AdminUser) (User, error) {
	cfg, err := s.Config(ctx, p)
	if err != nil {
		return User{}, err
	}
	var hash string
	if in.Password != nil {
		if err := checkPassword(cfg, *in.Password); err != nil {
			return User{}, err
		}
		if hash, err = s.hasher.HashPolicy(ctx, *in.Password); err != nil {
			return User{}, err
		}
	}
	var claims json.RawMessage
	if in.CustomClaims != nil {
		if claims, err = validClaims(*in.CustomClaims); err != nil {
			return User{}, err
		}
	}
	var u User
	err = s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		raw, err := q.GetUserForUpdate(ctx, queries.GetUserForUpdateParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: in.UID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return err
		}
		upd := queries.UpdateUserParams{
			ProjectNumber: p.Number, TenantID: p.Tenant, Uid: in.UID, Email: raw.Email, EmailVerified: raw.EmailVerified,
			DisplayName: raw.DisplayName, PhotoUrl: raw.PhotoUrl, Disabled: raw.Disabled, CustomClaims: raw.CustomClaims,
		}
		revoke := in.RevokeTokens || hash != ""
		if in.Email != nil {
			switch email, ok := normalizeEmail(*in.Email); {
			case *in.Email == "":
				upd.Email = nil
			case !ok:
				return ErrInvalidEmail
			case !strings.EqualFold(email, str(raw.Email)):
				upd.Email, revoke = &email, true
				if in.EmailVerified == nil {
					upd.EmailVerified = false
				}
			default:
				upd.Email = &email
			}
		}
		if in.DisplayName != nil {
			if !validDisplayName(*in.DisplayName) {
				return ErrInvalidDisplayName
			}
			upd.DisplayName = ptr(*in.DisplayName)
		}
		if in.PhotoURL != nil {
			if !validPhotoURL(*in.PhotoURL) {
				return ErrInvalidPhotoURL
			}
			upd.PhotoUrl = ptr(*in.PhotoURL)
		}
		if in.EmailVerified != nil {
			upd.EmailVerified = *in.EmailVerified
		}
		if in.Disabled != nil {
			upd.Disabled = *in.Disabled
		}
		if in.CustomClaims != nil {
			upd.CustomClaims = claims
		}
		if raw, err = q.UpdateUser(ctx, upd); err != nil {
			return err
		}
		if len(in.DeleteProviders) > 0 {
			if err := q.DeleteProviders(ctx, queries.DeleteProvidersParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: in.UID, ProviderIds: in.DeleteProviders}); err != nil {
				return err
			}
			if slices.Contains(in.DeleteProviders, "password") {
				if err := q.DeletePassword(ctx, queries.DeletePasswordParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: in.UID}); err != nil {
					return err
				}
			}
		}
		email := str(upd.Email)
		switch {
		case hash != "":
			if email == "" {
				return ErrMissingEmail
			}
			if err := s.attachPassword(ctx, q, p, in.UID, email, hash); err != nil {
				return err
			}
		case in.Email != nil && email != "":
			// Keep the password provider's identifier in step with the email.
			if _, err := q.GetPassword(ctx, queries.GetPasswordParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: in.UID}); err == nil {
				if err := q.UpsertProvider(ctx, queries.UpsertProviderParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: in.UID, ProviderID: "password", RawID: email, Email: &email}); err != nil {
					return err
				}
			}
		}
		if revoke {
			if raw.TokensValidAfter, err = q.RevokeUserTokens(ctx, queries.RevokeUserTokensParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: in.UID}); err != nil {
				return err
			}
		}
		u, err = s.loadUser(ctx, q, p, raw)
		return err
	})
	if _, ok := store.UniqueViolation(err); ok {
		return User{}, ErrEmailExists
	}
	if err == nil {
		s.audit(ctx, s.q, p, in.UID, "admin.user.update", "ok", map[string]any{
			"disabled": in.Disabled, "revoked": in.RevokeTokens, "password": in.Password != nil, "claims": in.CustomClaims != nil,
		})
	}
	return u, err
}

// DeleteUser removes a user by uid.
func (s *Service) DeleteUser(ctx context.Context, p Project, uid string) error {
	u, err := s.GetUser(ctx, p, uid)
	if err != nil {
		return err
	}
	return s.deleteUser(ctx, p, u)
}

// DeleteUsers removes up to 1000 users. Unknown uids are not errors, as in deleteUsers; the
// result maps the index of each failed uid to its error.
func (s *Service) DeleteUsers(ctx context.Context, p Project, uids []string) (map[int]error, error) {
	if len(uids) > maxBatch {
		return nil, failf("TOO_MANY_IDENTIFIERS", "at most 1000 uids")
	}
	failed := map[int]error{}
	for i, uid := range uids {
		if !validUID(uid) {
			failed[i] = ErrInvalidUID
			continue
		}
		if err := s.DeleteUser(ctx, p, uid); err != nil && !errors.Is(err, ErrUserNotFound) {
			failed[i] = err
		}
	}
	return failed, nil
}

// UpdateConfig changes the project's sign-in settings; fn edits a copy of the current ones.
func (s *Service) UpdateConfig(ctx context.Context, p Project, fn func(*queries.AuthProjectConfig) error) (queries.AuthProjectConfig, error) {
	c, err := s.Config(ctx, p)
	if err != nil {
		return c, err
	}
	if err := fn(&c); err != nil {
		return c, err
	}
	if c.PasswordMinLength < 6 || c.PasswordMinLength > 30 {
		return c, failf("INVALID_CONFIG", "password minimum length must be 6 to 30")
	}
	if c.PasswordEnforcement != "OFF" && c.PasswordEnforcement != "ENFORCE" {
		return c, failf("INVALID_CONFIG", "password enforcement must be OFF or ENFORCE")
	}
	if err := checkConfig(&c); err != nil {
		return c, err
	}
	out, err := s.q.UpdateProjectConfig(ctx, queries.UpdateProjectConfigParams{
		ProjectNumber: p.Number, PasswordEnabled: c.PasswordEnabled, AnonymousEnabled: c.AnonymousEnabled,
		ClientSignupEnabled: c.ClientSignupEnabled, EnumerationProtection: c.EnumerationProtection,
		AuthorizedDomains: c.AuthorizedDomains, PasswordMinLength: c.PasswordMinLength,
		PasswordRequireLower: c.PasswordRequireLower, PasswordRequireUpper: c.PasswordRequireUpper,
		PasswordRequireDigit: c.PasswordRequireDigit, PasswordRequireSymbol: c.PasswordRequireSymbol,
		PasswordEnforcement: c.PasswordEnforcement, AppName: c.AppName,
	})
	if err == nil {
		s.audit(ctx, s.q, p, "", "config.update", "ok", nil)
	}
	return out, err
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefBool(p *bool) bool { return p != nil && *p }
