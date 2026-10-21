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
)

// A REST request names its credential in one of three places, as Firebase documents and the
// SDKs send them: Authorization: Bearer <token> (the Admin SDKs, firebase-tools), ?access_token=
// (an OAuth token in the URL), or ?auth= (an ID token, as the JS SDK's REST client and curl
// users send it). The first present wins, in that order. A request without one acts as nobody.
//
// The credential decides who the request acts as, as production decides it (wire golden
// production/rest_auth.json and auth_expired.json):
//
//   - A bearer or ?access_token= is an admin credential, a service account's access token or
//     the owner's personal CLI token, and bypasses rules. With auth_variable_override=<JSON>
//     it acts as that auth variable instead, as the Go and Python Admin SDKs ask with
//     databaseAuthVariableOverride (db.go:35, db.py:818), and with auth_variable_override=null
//     as nobody. The override is honoured only beside an admin credential; from anyone else
//     it is ignored, as on the emulator. Anything else there, a user's ID token included,
//     fails the request with 401 "Unauthorized request.".
//   - ?auth= is an Aruma Auth user's ID token, verified for the database's project. One that
//     does not verify, expired, forged or not a token at all, is ignored: the request acts as
//     nobody, so a public path still answers and a protected one is "Permission denied".
//
// The emulator takes any bearer as its owner, and ignores a bad ?auth= as production does.

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

var errUnauthorizedRequest = restErr(http.StatusUnauthorized, "Unauthorized request.")

// openREST opens the session one REST request acts in, signed in with its credential. The
// caller closes it.
func (a *API) openREST(ctx context.Context, r *http.Request, t core.Target) (*core.Session, error) {
	opts := core.OpenOptions{Loopback: httpx.PeerIsLoopback(r)}
	token, inAuth := restCredential(r)
	if token == "" {
		return a.svc.Open(t, core.Auth{}, opts)
	}
	if inAuth {
		s, err := a.svc.Open(t, core.Auth{}, opts)
		if err != nil {
			return nil, err
		}
		if isAdminToken(token) {
			return s, nil // not an ID token: ignored
		}
		if err := s.SignIn(ctx, token); err != nil {
			var ce *core.Error
			switch {
			case idtoken.Status(err) != "":
				return s, nil // refused: ignored, the request acts as nobody
			case errors.As(err, &ce):
				s.Close()
				return nil, err
			}
			s.Close()
			a.log.Warn("rtdb: verifying an ID token", "database", t.Name, "error", err)
			return nil, core.ErrUnavailable
		}
		return s, nil
	}
	if !isAdminToken(token) {
		return nil, errUnauthorizedRequest
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
