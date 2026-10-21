package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/services/rtdb/core"
	"github.com/arumadev/aruma/internal/services/rtdb/wire"
)

// A REST request names its credential in one of three places, as Firebase documents and the
// SDKs send them: Authorization: Bearer <token> (the Admin SDKs, firebase-tools), ?access_token=
// (an OAuth token in the URL), or ?auth= (an ID token, as the JS SDK's REST client and curl
// users send it). The first present wins, in that order. A request without one acts as nobody.
//
// The credential decides who the request acts as, as on the WebSocket (auth.go):
//
//   - An admin credential, a service account's access token or the owner's personal CLI
//     token, bypasses rules. With auth_variable_override=<JSON> it acts as that auth variable
//     instead, as the Go and Python Admin SDKs ask with databaseAuthVariableOverride (db.go:35,
//     db.py:818), and with auth_variable_override=null as nobody. The override is honoured
//     only beside an admin credential; from anyone else it is ignored, as on the emulator.
//   - Anything else is an Aruma Auth user's ID token, verified for the database's project.
//
// A credential that does not verify fails the request with 401. The emulator ignores a bad
// ?auth= and takes any bearer as the owner; production refuses them, and so does Aruma. The
// texts follow the protocol's (B-emulator-oracle.md §1.8).

// restCredential returns the request's credential, "" when it carries none, and whether it
// came in ?auth=.
func restCredential(r *http.Request) (token string, inAuth bool) {
	if scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " "); ok && strings.EqualFold(scheme, "Bearer") {
		if token = strings.TrimSpace(token); token != "" {
			return token, false
		}
	}
	q := r.URL.Query()
	if t := q.Get("access_token"); t != "" {
		return t, false
	}
	return q.Get("auth"), q.Get("auth") != ""
}

// isAdminToken reports whether token has the form of an admin credential.
func isAdminToken(token string) bool {
	return strings.HasPrefix(token, authn.ServiceTokenPrefix) || strings.HasPrefix(token, authn.AccessTokenPrefix)
}

var (
	errUnauthorizedRequest = restErr(http.StatusUnauthorized, "Unauthorized request.")
	errUnparsableToken     = restErr(http.StatusUnauthorized, "Could not parse auth token.")
	errExpiredToken        = restErr(http.StatusUnauthorized, "Auth token is expired.")
)

// openREST opens the session one REST request acts in, signed in with its credential. The
// caller closes it.
func (a *API) openREST(ctx context.Context, r *http.Request, t core.Target) (*core.Session, error) {
	opts := core.OpenOptions{Loopback: httpx.PeerIsLoopback(r)}
	token, inAuth := restCredential(r)
	if token == "" {
		return a.svc.Open(t, core.Auth{}, opts)
	}
	if inAuth && isAdminToken(token) {
		// ?auth= carries ID tokens: the emulator does not take its owner credential there
		// either.
		return nil, errUnparsableToken
	}
	if !isAdminToken(token) {
		s, err := a.svc.Open(t, core.Auth{}, opts)
		if err != nil {
			return nil, err
		}
		if err := s.SignIn(ctx, token); err != nil {
			s.Close()
			var ce *core.Error
			switch status := idtoken.Status(err); {
			case status == wire.StatusExpiredToken:
				return nil, errExpiredToken
			case status != "":
				return nil, errUnparsableToken
			case errors.As(err, &ce):
				return nil, err
			}
			a.log.Warn("rtdb: verifying an ID token", "database", t.Name, "error", err)
			return nil, core.ErrUnavailable
		}
		return s, nil
	}
	p, err := a.verifyAdmin(ctx, t, token)
	if err != nil {
		if refused(err) {
			a.log.Info("rtdb: admin credential refused", "database", t.Name, "error", err)
			return nil, errUnauthorizedRequest
		}
		a.log.Warn("rtdb: verifying an admin credential", "database", t.Name, "error", err)
		return nil, core.ErrUnavailable
	}
	auth := core.Auth{Admin: true, Account: p.Email}
	if q := r.URL.Query(); q.Has("auth_variable_override") {
		raw := json.RawMessage(q.Get("auth_variable_override"))
		if !json.Valid(raw) {
			return nil, core.ErrInvalidAuthVariable
		}
		// null acts as nobody; an object as that auth variable (Session.SetAuth checks it).
		auth = core.Auth{}
		if string(raw) != "null" {
			auth.Variable = raw
		}
	}
	return a.svc.Open(t, auth, opts)
}
