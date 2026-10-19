package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/platform/crypto"
	"github.com/osama300/aurma-project/internal/services/auth/queries"
)

// User is an Auth user with its providers.
type User struct {
	UID              string
	Email            string
	EmailVerified    bool
	DisplayName      string
	PhotoURL         string
	Disabled         bool
	CustomClaims     json.RawMessage
	TokensValidAfter time.Time
	CreatedAt        time.Time
	LastLoginAt      *time.Time
	LastRefreshAt    *time.Time
	HasPassword      bool
	PasswordUpdated  *time.Time
	Providers        []Provider
}

// Provider is a linked sign-in method.
type Provider struct {
	ProviderID  string
	RawID       string
	Email       string
	DisplayName string
	PhotoURL    string
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func ptr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func toUser(u queries.AuthUser) User {
	return User{
		UID: u.Uid, Email: str(u.Email), EmailVerified: u.EmailVerified, DisplayName: str(u.DisplayName),
		PhotoURL: str(u.PhotoUrl), Disabled: u.Disabled, CustomClaims: u.CustomClaims,
		TokensValidAfter: u.TokensValidAfter, CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt, LastRefreshAt: u.LastRefreshAt,
	}
}

// loadUser reads a user with its providers and password state.
func (s *Service) loadUser(ctx context.Context, q *queries.Queries, p Project, u queries.AuthUser) (User, error) {
	out := toUser(u)
	providers, err := q.ListProviders(ctx, queries.ListProvidersParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.Uid})
	if err != nil {
		return out, err
	}
	for _, pr := range providers {
		out.Providers = append(out.Providers, Provider{ProviderID: pr.ProviderID, RawID: pr.RawID, Email: str(pr.Email), DisplayName: str(pr.DisplayName), PhotoURL: str(pr.PhotoUrl)})
	}
	pw, err := q.GetPassword(ctx, queries.GetPasswordParams{ProjectNumber: p.Number, TenantID: p.Tenant, Uid: u.Uid})
	switch {
	case err == nil:
		out.HasPassword = true
		out.PasswordUpdated = &pw.UpdatedAt
	case !errors.Is(err, pgx.ErrNoRows):
		return out, err
	}
	return out, nil
}

// SignInProvider is the provider an anonymous or password user is known by.
func (u User) SignInProvider() string {
	if len(u.Providers) == 0 && u.Email == "" {
		return "anonymous"
	}
	return "password"
}

// newUID returns a 28-character uid, the shape Firebase generates.
func newUID() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := crypto.RandomBytes(28)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func normalizeEmail(raw string) (string, bool) {
	e := strings.TrimSpace(raw)
	if e == "" || len(e) > 254 {
		return "", false
	}
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e || !strings.Contains(e[strings.LastIndex(e, "@"):], ".") {
		return "", false
	}
	return e, true
}

func validDisplayName(s string) bool { return utf8.ValidString(s) && utf8.RuneCountInString(s) <= 256 }

func validPhotoURL(s string) bool {
	return s == "" || (len(s) <= 2048 && (strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")))
}

// reservedClaims may not be set as custom claims (Firebase's list).
var reservedClaims = map[string]bool{
	"acr": true, "amr": true, "at_hash": true, "aud": true, "auth_time": true, "azp": true, "cnf": true,
	"c_hash": true, "exp": true, "iat": true, "iss": true, "jti": true, "nbf": true, "nonce": true,
	"sub": true, "firebase": true, "user_id": true,
}

// validClaims checks a custom claims object: JSON object, at most 1000 bytes, no reserved names.
func validClaims(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return nil, nil
	}
	if len(raw) > 1000 {
		return nil, ErrClaimsTooLarge
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, ErrInvalidClaims
	}
	for k := range m {
		if reservedClaims[k] {
			return nil, failf("FORBIDDEN_CLAIM", k)
		}
	}
	compact, _ := json.Marshal(m)
	return compact, nil
}

// checkPassword applies the project's password policy.
func checkPassword(cfg queries.AuthProjectConfig, pw string) error {
	if utf8.RuneCountInString(pw) < int(cfg.PasswordMinLength) {
		return weakPassword(int(cfg.PasswordMinLength))
	}
	if len(pw) > crypto.MaxPasswordLen {
		return failf("WEAK_PASSWORD", "Password should be at most "+strconv.Itoa(crypto.MaxPasswordLen)+" bytes")
	}
	if cfg.PasswordEnforcement != "ENFORCE" {
		return nil
	}
	var lower, upper, digit, symbol bool
	for _, r := range pw {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			symbol = true
		}
	}
	var missing []string
	if cfg.PasswordRequireLower && !lower {
		missing = append(missing, "lower case character")
	}
	if cfg.PasswordRequireUpper && !upper {
		missing = append(missing, "upper case character")
	}
	if cfg.PasswordRequireDigit && !digit {
		missing = append(missing, "numeric character")
	}
	if cfg.PasswordRequireSymbol && !symbol {
		missing = append(missing, "non-alphanumeric character")
	}
	if len(missing) > 0 {
		return failf(ErrPasswordRequirements.Code, "Missing password requirements: ["+strings.Join(missing, ", ")+"]")
	}
	return nil
}
