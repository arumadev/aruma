package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/services/rtdb/core"
	"github.com/arumadev/aruma/internal/services/rtdb/wire"
)

// revalidateEvery is how often an admin credential is checked again while a connection
// uses it, so a revoked token or a deleted service account stops working within it.
var revalidateEvery = 5 * time.Minute

// adminState is the admin credential a session signed in with. Guarded by session.mu.
type adminState struct {
	token   string
	expires time.Time // zero: the credential does not expire
	gen     uint64    // changes with every sign-in and sign-out, so stale checks stop
	timer   *time.Timer
}

func (a *adminState) stop() {
	if a.timer != nil {
		a.timer.Stop()
	}
	a.gen++
	a.token, a.expires, a.timer = "", time.Time{}, nil
}

// authenticate handles auth and gauth; the caller re-checks the listens before the reply
// goes out. Two kinds of credential sign a session in:
//
//   - An Aruma Auth user's ID token, which idtoken.Verifier checks (core.Session.SignIn). The
//     session then acts as that user, auth = {provider, uid, token}, until the token expires
//     or is revoked, when the client is told with ac and the session becomes
//     unauthenticated (core's revoke.go). The reply carries the auth variable and the token's
//     expiry, as Firebase's does.
//   - An admin credential: a service account's access token for this project (what the Admin
//     SDKs send, from Aruma's OAuth endpoint), or a personal CLI token of the project's owner.
//     It bypasses rules, as admin credentials do in Firebase, until it expires or stops
//     verifying (checked every revalidateEvery), when the client is told with ac and the
//     session becomes unauthenticated. An Admin SDK that also sends an auth variable override
//     (authvar) or noauth asks to act as that client instead: its credential must verify, and
//     the rules then see the override, or null. The credential is checked again as an
//     admin's is, and when it fails the session is told with ac and acts as nobody.
//
// gauth is for admin credentials only: production answers gauth with a user's ID token
// permission_denied (testdata/golden/production/auth.json in the wire package), where the
// emulator signs in as admin and Aruma used to sign the user in.
//
// A credential that does not verify signs the session out: it acts as nobody until one does.
// (Production keeps the session's previous sign-in instead, as the emulator does; the SDKs
// close the connection on any refusal, PersistentConnection.onAuthRevoked_, so only a raw
// client could tell.) A token refused as such is answered with its status (idtoken.Status)
// and production's text where it is known; a failure to check it at all, such as the
// database being down, is answered unavailable, since it says nothing about the token.
func (s *session) authenticate(ctx context.Context, b wire.AuthBody, gauth bool) wire.Reply {
	token := strings.TrimSpace(b.Credential)
	if !strings.HasPrefix(token, authn.ServiceTokenPrefix) && !strings.HasPrefix(token, authn.AccessTokenPrefix) {
		if gauth {
			s.signOut()
			return wire.Fail(wire.StatusPermissionDenied, "Permission denied")
		}
		return s.signInUser(ctx, token)
	}
	p, err := s.api.verifyAdmin(ctx, s.target, token)
	if err != nil {
		s.signOut()
		s.api.log.Info("rtdb: admin credential refused", "database", s.target.Name, "error", err)
		return wire.Fail(wire.StatusInvalidToken, "Invalid token.")
	}
	var expires any
	if !p.ExpiresAt.IsZero() {
		expires = p.ExpiresAt.Unix()
	}
	auth := core.Auth{Admin: true, Account: p.Email}
	if b.NoAuth || hasAuthVar(b) {
		s.signOut()
		auth = core.Auth{Override: true}
		if hasAuthVar(b) {
			auth.Variable = b.AuthVar
		}
	}
	if err := s.core.SetAuth(auth); err != nil {
		e := core.AsError(err)
		return wire.Fail(e.Status, e.Message)
	}
	// The credential is checked again while the session uses it, as itself or as the client
	// it asked to act as.
	s.mu.Lock()
	s.admin.stop()
	s.admin.token, s.admin.expires = token, p.ExpiresAt
	s.scheduleCheck()
	s.mu.Unlock()
	return wire.OKWith(map[string]any{"auth": nil, "expires": expires})
}

// signInUser signs the session in with a user's ID token.
func (s *session) signInUser(ctx context.Context, token string) wire.Reply {
	s.mu.Lock()
	s.admin.stop()
	s.mu.Unlock()
	if err := s.core.SignIn(ctx, token); err != nil {
		_ = s.core.SetAuth(core.Auth{})
		var ce *core.Error
		switch status := idtoken.Status(err); {
		case status == wire.StatusExpiredToken:
			return wire.Fail(status, "Auth token is expired")
		case status != "":
			return wire.Fail(status, refusalText(token))
		case errors.As(err, &ce):
			return wire.Fail(ce.Status, ce.Message)
		}
		s.api.log.Warn("rtdb: verifying an ID token", "database", s.target.Name, "error", err)
		e := core.ErrUnavailable
		return wire.Fail(e.Status, e.Message)
	}
	return wire.OKWith(map[string]any{"auth": s.core.AuthVariable(), "expires": s.core.Auth().User.Expires.Unix()})
}

// refusalText is the text of an invalid_token reply, as production words it
// (production/auth.json): "Could not parse auth token." for what is not a JWT, "Unsupported
// jwt algorithm: none" for an unsigned one. Production's text for a signed token that does not
// verify is not known; Aruma says "Invalid token.".
func refusalText(token string) string {
	head, _, ok := strings.Cut(token, ".")
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(head, "="))
	var h struct {
		Alg string `json:"alg"`
	}
	if !ok || err != nil || json.Unmarshal(raw, &h) != nil {
		return "Could not parse auth token."
	}
	if h.Alg == "none" {
		return "Unsupported jwt algorithm: none"
	}
	return "Invalid token."
}

func hasAuthVar(b wire.AuthBody) bool {
	return len(b.AuthVar) > 0 && string(b.AuthVar) != "null"
}

// signOut makes the session unauthenticated and stops checking its admin credential.
func (s *session) signOut() {
	s.mu.Lock()
	s.admin.stop()
	s.mu.Unlock()
	_ = s.core.SetAuth(core.Auth{})
}

// scheduleCheck arms the next check of the admin credential: at its expiry, or after
// revalidateEvery, whichever comes first. Callers hold s.mu.
func (s *session) scheduleCheck() {
	wait := revalidateEvery
	if !s.admin.expires.IsZero() {
		wait = min(wait, time.Until(s.admin.expires))
	}
	gen := s.admin.gen
	s.admin.timer = time.AfterFunc(max(wait, 0), func() { s.checkAdmin(gen) })
}

// checkAdmin re-verifies the admin credential. An expired or refused credential is revoked:
// the client gets ac and the session loses admin; it signs in again with a fresh token, as
// the SDKs do on ac (PersistentConnection.ts:985-1004). A failure to check (the database is
// down) leaves things as they are until the next check.
func (s *session) checkAdmin(gen uint64) {
	s.mu.Lock()
	if s.closed || s.admin.gen != gen {
		s.mu.Unlock()
		return
	}
	token, expires := s.admin.token, s.admin.expires
	s.mu.Unlock()

	status, reason := "", ""
	if !expires.IsZero() && !time.Now().Before(expires) {
		status, reason = wire.StatusExpiredToken, "Auth token is expired."
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		p, err := s.api.verifyAdmin(ctx, s.target, token)
		cancel()
		switch {
		case err == nil:
			expires = p.ExpiresAt
		case refused(err):
			status, reason = wire.StatusInvalidToken, "Auth token is no longer valid."
		default:
			s.api.log.Warn("rtdb: re-checking an admin credential", "database", s.target.Name, "error", err)
		}
	}

	s.mu.Lock()
	if s.closed || s.admin.gen != gen {
		s.mu.Unlock()
		return
	}
	if status == "" {
		s.admin.expires = expires
		s.scheduleCheck()
		s.mu.Unlock()
		return
	}
	s.admin.stop()
	// Revoke queues on the writer, so not under s.mu. A sign-in that slips in between is
	// a new credential, which Revoke would wrongly end: core ends only a sign-in an admin
	// credential vouched for, the admin's own or the client's it acted as.
	s.mu.Unlock()
	s.core.RevokeIf(func(a core.Auth) bool { return a.Admin || a.Override }, status, reason)
}

// refused reports whether a verification error means the credential is no good, rather than
// that it could not be checked.
func refused(err error) bool {
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		return false
	}
	switch ae.Code {
	case apierr.Unauthenticated, apierr.PermissionDenied, apierr.NotFound:
		return true
	}
	return false
}

// verifyAdmin checks an admin credential and that it may act on the target's project: a
// service account of that project, or an account that owns it.
func (a *API) verifyAdmin(ctx context.Context, t core.Target, token string) (*authn.Principal, error) {
	if a.opts.Credentials == nil || a.opts.Projects == nil {
		return nil, apierr.New(apierr.Unauthenticated, "admin credentials are not configured")
	}
	var (
		p   *authn.Principal
		err error
	)
	if strings.HasPrefix(token, authn.ServiceTokenPrefix) {
		p, err = a.opts.Credentials.VerifyServiceToken(ctx, token)
	} else {
		p, err = a.opts.Credentials.VerifyAccessToken(ctx, token)
	}
	if err != nil {
		return nil, err
	}
	if _, err := a.opts.Projects.GetProject(ctx, p, t.ProjectID); err != nil {
		return nil, err
	}
	return p, nil
}
