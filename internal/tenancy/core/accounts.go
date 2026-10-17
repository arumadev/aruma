package core

import (
	"context"
	"net/mail"
	"net/netip"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/crypto"
	"github.com/osama300/aurma-project/internal/store"
	"github.com/osama300/aurma-project/internal/tenancy/queries"
)

// Account is a project owner as the APIs show it.
type Account struct {
	ID          uuid.UUID
	Email       string
	DisplayName string
	Locale      string
	Disabled    bool
	CreatedAt   time.Time
}

func toAccount(a queries.ControlAccount) Account {
	return Account{ID: a.ID, Email: a.Email, DisplayName: a.DisplayName, Locale: a.Locale, Disabled: a.Disabled, CreatedAt: a.CreatedAt}
}

// NewAccount is the input for creating an account.
type NewAccount struct {
	Email       string
	Password    string
	DisplayName string
	Locale      string
}

// LoginResult is a new console session. Token goes into the cookie; CSRFToken is handed to
// the page for the X-Aruma-CSRF header.
type LoginResult struct {
	Account   Account
	Token     string
	CSRFToken string
	ExpiresAt time.Time
}

// ClientInfo describes where a request came from, for session records.
type ClientInfo struct {
	IP        netip.Addr
	UserAgent string
}

var errNoSuchAccount = apierr.New(apierr.NotFound, "No account with this email exists.")

var errBadCredentials = apierr.New(apierr.Unauthenticated, "The email or password is incorrect.")

// SetupRequired reports whether the instance has no account yet.
func (s *Service) SetupRequired(ctx context.Context) (bool, error) {
	n, err := s.q.CountAccounts(ctx)
	if err != nil {
		return false, internal(err)
	}
	return n == 0, nil
}

// PrepareSetup issues the one-time setup code when the instance has no account. The caller
// prints it to the log once; a restart issues a new code and voids the old one (ADR-010 S-1).
func (s *Service) PrepareSetup(ctx context.Context) (code string, required bool, err error) {
	required, err = s.SetupRequired(ctx)
	if err != nil || !required {
		return "", false, err
	}
	code = crypto.RandomString(18)
	s.setupMu.Lock()
	s.setupHash = s.keys.HashToken("setup:" + code)
	s.setupMu.Unlock()
	return code, true, nil
}

// CompleteSetup creates the first account with the setup code and signs it in.
func (s *Service) CompleteSetup(ctx context.Context, code string, in NewAccount, client ClientInfo) (*LoginResult, error) {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	if s.setupHash == nil || !crypto.EqualHash(s.setupHash, s.keys.HashToken("setup:"+code)) {
		return nil, apierr.New(apierr.PermissionDenied, "The setup code is invalid or has already been used.")
	}
	acc, err := s.createAccount(ctx, in, true)
	if err != nil {
		return nil, err
	}
	s.setupHash = nil
	s.log.Info("first account created", "account_id", acc.ID.String())
	return s.startSession(ctx, acc, client)
}

// Signup creates an account from the console when the operator has opened sign-up.
func (s *Service) Signup(ctx context.Context, in NewAccount, client ClientInfo) (*LoginResult, error) {
	if !s.opts.SignupOpen {
		return nil, apierr.New(apierr.PermissionDenied, "Sign-up is closed on this server. Ask the operator for an account.")
	}
	acc, err := s.createAccount(ctx, in, false)
	if err != nil {
		return nil, err
	}
	return s.startSession(ctx, acc, client)
}

// CreateAccount creates an account for the operator command `aruma accounts create`.
func (s *Service) CreateAccount(ctx context.Context, in NewAccount) (Account, error) {
	acc, err := s.createAccount(ctx, in, false)
	if err != nil {
		return Account{}, err
	}
	return toAccount(acc), nil
}

func (s *Service) createAccount(ctx context.Context, in NewAccount, mustBeFirst bool) (queries.ControlAccount, error) {
	email, err := validEmail(in.Email)
	if err != nil {
		return queries.ControlAccount{}, err
	}
	name := strings.TrimSpace(in.DisplayName)
	if utf8.RuneCountInString(name) > 100 {
		return queries.ControlAccount{}, apierr.Invalid("displayName", "Display name must be at most 100 characters.")
	}
	locale, err := validLocale(in.Locale)
	if err != nil {
		return queries.ControlAccount{}, err
	}
	hash, err := s.hasher.Hash(ctx, in.Password)
	if err != nil {
		if err == crypto.ErrPasswordLength {
			return queries.ControlAccount{}, apierr.Invalid("password", "Password must be 8 to 1024 characters.")
		}
		return queries.ControlAccount{}, internal(err)
	}

	var acc queries.ControlAccount
	err = s.tx(ctx, func(q *queries.Queries, tx pgx.Tx) error {
		if mustBeFirst {
			// Serialise concurrent setups: only one may see an empty table.
			if _, err := tx.Exec(ctx, "LOCK TABLE control.accounts IN SHARE ROW EXCLUSIVE MODE"); err != nil {
				return err
			}
			n, err := q.CountAccounts(ctx)
			if err != nil {
				return err
			}
			if n > 0 {
				return apierr.New(apierr.PermissionDenied, "The setup code is invalid or has already been used.")
			}
		}
		acc, err = q.CreateAccount(ctx, queries.CreateAccountParams{
			Email: email, PasswordHash: hash, DisplayName: name, Locale: locale,
		})
		return err
	})
	if _, ok := store.UniqueViolation(err); ok {
		return acc, apierr.New(apierr.AlreadyExists, "An account with this email already exists.")
	}
	return acc, internal(err)
}

// Login checks credentials and starts a session. Unknown accounts, wrong passwords and
// disabled accounts get the same answer after the same work (ADR-010 S-7).
func (s *Service) Login(ctx context.Context, email, password string, client ClientInfo) (*LoginResult, error) {
	acc, err := s.q.GetAccountByEmail(ctx, strings.TrimSpace(email))
	if err != nil {
		if !notFound(err) {
			return nil, internal(err)
		}
		s.hasher.VerifyDummy(ctx, password)
		return nil, errBadCredentials
	}
	ok, rehash, err := s.hasher.Verify(ctx, password, acc.PasswordHash)
	if err != nil {
		return nil, internal(err)
	}
	if !ok || acc.Disabled {
		return nil, errBadCredentials
	}
	if rehash {
		if h, err := s.hasher.Hash(ctx, password); err == nil {
			_ = s.q.SetAccountPassword(ctx, queries.SetAccountPasswordParams{ID: acc.ID, PasswordHash: h})
		}
	}
	return s.startSession(ctx, acc, client)
}

func (s *Service) startSession(ctx context.Context, acc queries.ControlAccount, client ClientInfo) (*LoginResult, error) {
	token := crypto.NewToken("")
	now := time.Now()
	var ip *netip.Addr
	if client.IP.IsValid() {
		ip = &client.IP
	}
	ua := truncate(client.UserAgent, 300)
	sess, err := s.q.CreateSession(ctx, queries.CreateSessionParams{
		AccountID:         acc.ID,
		TokenHash:         s.keys.HashToken(token),
		Ip:                ip,
		UserAgent:         &ua,
		IdleExpiresAt:     now.Add(s.opts.IdleTimeout),
		AbsoluteExpiresAt: now.Add(s.opts.AbsoluteTimeout),
	})
	if err != nil {
		return nil, internal(err)
	}
	return &LoginResult{
		Account:   toAccount(acc),
		Token:     token,
		CSRFToken: s.keys.CSRFToken(token),
		ExpiresAt: sess.AbsoluteExpiresAt,
	}, nil
}

// Logout revokes the caller's current session.
func (s *Service) Logout(ctx context.Context, p *authn.Principal) error {
	if p.Method != authn.MethodSession {
		return nil
	}
	_, err := s.q.RevokeSession(ctx, queries.RevokeSessionParams{ID: p.SessionID, AccountID: p.AccountID})
	return internal(err)
}

// Me returns the caller's account.
func (s *Service) Me(ctx context.Context, p *authn.Principal) (Account, error) {
	if p.Kind != authn.KindAccount {
		return Account{}, errNotFound()
	}
	acc, err := s.q.GetAccount(ctx, p.AccountID)
	if err != nil {
		if notFound(err) {
			return Account{}, errNotFound()
		}
		return Account{}, internal(err)
	}
	return toAccount(acc), nil
}

// UpdateProfile changes the caller's display name and/or locale.
func (s *Service) UpdateProfile(ctx context.Context, p *authn.Principal, displayName, locale *string) (Account, error) {
	if displayName != nil {
		n := strings.TrimSpace(*displayName)
		if utf8.RuneCountInString(n) > 100 {
			return Account{}, apierr.Invalid("displayName", "Display name must be at most 100 characters.")
		}
		displayName = &n
	}
	if locale != nil {
		l, err := validLocale(*locale)
		if err != nil {
			return Account{}, err
		}
		locale = &l
	}
	acc, err := s.q.UpdateAccountProfile(ctx, queries.UpdateAccountProfileParams{ID: p.AccountID, DisplayName: displayName, Locale: locale})
	if err != nil {
		return Account{}, internal(err)
	}
	return toAccount(acc), nil
}

// ChangePassword replaces the caller's password after checking the current one, signs out
// every other session (AC-6) and revokes every CLI token.
func (s *Service) ChangePassword(ctx context.Context, p *authn.Principal, current, next string) error {
	acc, err := s.q.GetAccount(ctx, p.AccountID)
	if err != nil {
		return internal(err)
	}
	ok, _, err := s.hasher.Verify(ctx, current, acc.PasswordHash)
	if err != nil {
		return internal(err)
	}
	if !ok {
		return apierr.Invalid("currentPassword", "The current password is incorrect.")
	}
	hash, err := s.hasher.Hash(ctx, next)
	if err != nil {
		if err == crypto.ErrPasswordLength {
			return apierr.Invalid("newPassword", "Password must be 8 to 1024 characters.")
		}
		return internal(err)
	}
	return internal(s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		if err := q.SetAccountPassword(ctx, queries.SetAccountPasswordParams{ID: acc.ID, PasswordHash: hash}); err != nil {
			return err
		}
		if err := q.RevokeOtherSessions(ctx, queries.RevokeOtherSessionsParams{AccountID: acc.ID, KeepID: p.SessionID}); err != nil {
			return err
		}
		// A token minted from a stolen session must not survive the owner's recovery.
		return q.RevokeAllAccessTokens(ctx, acc.ID)
	}))
}

// CheckPassword verifies the caller's current password, for operations that need a fresh
// proof of identity beyond the session.
func (s *Service) CheckPassword(ctx context.Context, p *authn.Principal, password string) error {
	acc, err := s.q.GetAccount(ctx, p.AccountID)
	if err != nil {
		return internal(err)
	}
	ok, _, err := s.hasher.Verify(ctx, password, acc.PasswordHash)
	if err != nil {
		return internal(err)
	}
	if !ok {
		return apierr.Invalid("currentPassword", "The current password is incorrect.")
	}
	return nil
}

// ResetPassword sets a new password for the operator command, signs the account out
// everywhere and revokes its CLI tokens.
func (s *Service) ResetPassword(ctx context.Context, email, password string) error {
	acc, err := s.q.GetAccountByEmail(ctx, email)
	if err != nil {
		if notFound(err) {
			return errNoSuchAccount // operator commands only; revealing existence is fine here
		}
		return internal(err)
	}
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		if err == crypto.ErrPasswordLength {
			return apierr.Invalid("password", "Password must be 8 to 1024 characters.")
		}
		return internal(err)
	}
	return internal(s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		if err := q.SetAccountPassword(ctx, queries.SetAccountPasswordParams{ID: acc.ID, PasswordHash: hash}); err != nil {
			return err
		}
		if err := q.RevokeAllSessions(ctx, acc.ID); err != nil {
			return err
		}
		return q.RevokeAllAccessTokens(ctx, acc.ID)
	}))
}

// SetDisabled disables or re-enables an account for the operator. Disabling signs it out
// everywhere and revokes its CLI tokens.
func (s *Service) SetDisabled(ctx context.Context, email string, disabled bool) error {
	acc, err := s.q.GetAccountByEmail(ctx, email)
	if err != nil {
		if notFound(err) {
			return errNoSuchAccount // operator commands only; revealing existence is fine here
		}
		return internal(err)
	}
	return internal(s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		if err := q.SetAccountDisabled(ctx, queries.SetAccountDisabledParams{ID: acc.ID, Disabled: disabled}); err != nil {
			return err
		}
		if !disabled {
			return nil
		}
		if err := q.RevokeAllSessions(ctx, acc.ID); err != nil {
			return err
		}
		return q.RevokeAllAccessTokens(ctx, acc.ID)
	}))
}

func validEmail(raw string) (string, error) {
	e := strings.TrimSpace(raw)
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Address != e || len(e) > 254 || !strings.Contains(e[strings.LastIndex(e, "@"):], ".") {
		return "", apierr.Invalid("email", "Enter a valid email address.")
	}
	return e, nil
}

func validLocale(l string) (string, error) {
	switch l {
	case "":
		return "ar", nil
	case "ar", "en":
		return l, nil
	}
	return "", apierr.Invalid("locale", "Locale must be ar or en.")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
