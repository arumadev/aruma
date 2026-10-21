package core

import (
	"encoding/json"
	"net/http"

	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/services/rtdb/rules"
)

// Auth is who a session acts as, and so what its rules' auth variable holds: an admin, a user
// signed in with an ID token, an Admin SDK's auth variable override, or nobody.
type Auth struct {
	// Admin is an admin or service credential. It bypasses rules, as in Firebase.
	Admin bool
	// Account names the admin principal for the logs: a service account email or an account
	// id. Empty for clients.
	Account string
	// User is the verified ID token of a user's session, and Token the token itself, kept to
	// verify it again (revoke.go). auth is then {provider, uid, token}.
	User  *idtoken.Claims
	Token string
	// Variable is the auth variable an Admin SDK asked its rules to see instead of an admin
	// (databaseAuthVariableOverride, sent as authvar), kept as sent. Such a session is not an
	// admin: the rules see exactly this value as auth.
	Variable json.RawMessage
}

// ErrInvalidAuthVariable is an auth variable override that is not a JSON object.
var ErrInvalidAuthVariable = &Error{statusInvalidToken, "Invalid auth variable override.", http.StatusBadRequest}

// principal is a as the rules engine takes it.
func (a Auth) principal() (rules.Auth, error) {
	switch {
	case a.Admin:
		return rules.Admin(), nil
	case a.User != nil:
		return rules.User(a.User.UID, a.User.Token)
	case len(a.Variable) > 0 && string(a.Variable) != "null":
		var v map[string]any
		if err := json.Unmarshal(a.Variable, &v); err != nil || v == nil {
			return rules.Auth{}, ErrInvalidAuthVariable
		}
		p, err := rules.Variable(v)
		if err != nil {
			return rules.Auth{}, ErrInvalidAuthVariable
		}
		return p, nil
	}
	return rules.Unauthenticated(), nil
}

// signedIn reports a session that presented a credential that verified: an admin, a user, or
// an Admin SDK acting as a client.
func (a Auth) signedIn() bool { return a.Admin || a.User != nil || a.Variable != nil }

// AuthVariable returns the auth variable the rules see for a user's session, which the reply
// to auth carries as the emulator's does; nil for any other session.
func (s *Session) AuthVariable() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.auth.User == nil {
		return nil
	}
	return s.principal.Value()
}
