// Package idtoken is the contract between Aruma Auth and the data services that accept its ID
// tokens, as ADR-015 promised for the first second service (Realtime Database).
//
// Auth implements both halves and knows nothing of its consumers; a consumer holds only these
// interfaces; internal/app is the one place that connects them. archtest keeps services from
// importing each other, which is why the contract lives in platform.
//
//   - Verifier checks a token the way Firebase's verifyIdToken(token, checkRevoked=true) does:
//     signature, issuer, audience and times, then the user's live state (deleted, disabled,
//     revoked).
//   - Revocations tells a consumer, as it happens, that a user's tokens are no longer valid, so
//     it can end the sessions it authenticated earlier (RTDB's auth_revoked).
package idtoken

import (
	"context"
	"errors"
	"time"
)

// Verifier verifies Aruma Auth ID tokens.
type Verifier interface {
	// Verify checks an ID token for the project and returns its claims. A token that is not
	// accepted fails with one of the errors below (Status maps each to a status); any other
	// error is an outage, such as an unreachable database, and says nothing about the token.
	Verify(ctx context.Context, project int64, token string) (*Claims, error)
}

// Claims are a verified ID token.
type Claims struct {
	UID            string
	Tenant         string // firebase.tenant; empty outside a tenant
	SignInProvider string // firebase.sign_in_provider: password, anonymous, custom, google.com...
	Email          string
	EmailVerified  bool
	IssuedAt       time.Time
	AuthTime       time.Time // the sign-in this token's session started with
	Expires        time.Time
	// Token is the whole payload as JSON decodes it (numbers are float64), developer claims
	// included: auth.token in security rules.
	Token map[string]any
}

// Error is a token the Verifier does not accept.
type Error struct {
	reason string
	status string
}

func (e *Error) Error() string { return "idtoken: " + e.reason }

// Status is the Realtime Database status the error answers with: expired_token for an expired
// token, invalid_token for the rest. The SDKs treat both as an auth failure and fetch a new
// token; a revoked, disabled or deleted user then fails to refresh and is signed out.
func (e *Error) Status() string { return e.status }

// The reasons a token is refused.
var (
	ErrExpired      = &Error{"token expired", "expired_token"}
	ErrInvalid      = &Error{"invalid token", "invalid_token"} // malformed, foreign or forged
	ErrRevoked      = &Error{"token revoked", "invalid_token"} // issued before the user's tokensValidAfter
	ErrDisabled     = &Error{"user disabled", "invalid_token"}
	ErrUserNotFound = &Error{"user not found", "invalid_token"} // deleted since the token was issued
)

// Status returns the status err answers with, or "" when err is not a token error.
func Status(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.status
	}
	return ""
}

// Cause is why a user's tokens stopped being valid.
type Cause string

const (
	Disabled        Cause = "disabled"
	Deleted         Cause = "deleted"
	TokensRevoked   Cause = "tokens_revoked" // revokeRefreshTokens, the console, or a takeover by a verified provider
	PasswordChanged Cause = "password_changed"
	EmailChanged    Cause = "email_changed"
)

// Revocation says that some or all of a user's tokens are no longer valid.
type Revocation struct {
	Project int64
	Tenant  string
	UID     string
	Cause   Cause
	// ValidAfter is the user's new tokensValidAfter for the causes that keep the user
	// signed in somewhere: tokens whose session started before it are revoked, while the
	// session the change itself returned (a new password's) starts at it and survives. It is
	// zero for Disabled and Deleted, which end every token.
	ValidAfter time.Time
}

// Revokes reports whether r ends the session a token with claims c opened in project. Times
// compare at second precision, as the Verifier and Firebase's validSince do.
func (r Revocation) Revokes(project int64, c *Claims) bool {
	if c == nil || r.Project != project || r.UID != c.UID || r.Tenant != c.Tenant {
		return false
	}
	return r.ValidAfter.IsZero() || c.AuthTime.Unix() < r.ValidAfter.Unix()
}

// Revocations delivers the revocations of a project's users.
//
// Delivery is in-process: a revocation is published after the change commits, on the node that
// made it. Aruma runs one node per ADR-019, so that node sees every change; several nodes will
// need the changes to travel between them (Postgres NOTIFY or the events log), and this
// interface stays as it is.
type Revocations interface {
	// Subscribe starts delivering the project's revocations. The caller must Close the
	// subscription when it no longer listens.
	Subscribe(project int64) *Subscription
}
