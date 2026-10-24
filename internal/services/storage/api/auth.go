package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/services/storage/core"
)

// A /v0 request acts as whoever its Authorization names (A §2.3, §7; E §7):
//
//   - None, or an empty one: anonymous, request.auth = null in the rules. Not an error.
//   - "Firebase <idToken>": an Aruma Auth user of the bucket's project, verified by
//     idtoken.Verifier (signature, expiry, revocation). A token that does not verify is 401,
//     which every SDK reads as unauthenticated and answers by refreshing its token.
//   - "Bearer <access token>": an admin, which bypasses the rules (ADR-021 §7): a service
//     account's access token of the bucket's project, or the CLI token of an account that owns
//     it. That is how the console and the Admin SDKs call create_token (CT, E §5). A bearer
//     that is not such a token, a user's ID token included, is 401: production's answer to
//     "Bearer <idToken>" on /v0 is not known, and acting as that user would be a guess (E §7).
//   - Any other scheme is 401.
//
// X-Firebase-AppCheck and X-Firebase-GMPID are accepted and ignored in v0.3 (ADR-022 §19; JS
// never sends GMPID, A §2.3). The bucket is resolved before this runs, so a credential is only
// ever checked against an active project, and an unknown bucket is 404 whatever the credential
// (bucket_probe.json).

// principal returns who the request acts as on bucket b. A failure to check a credential at
// all, the database being down, is core.ErrUnavailable: it says nothing about the credential.
func (a *API) principal(ctx context.Context, r *http.Request, b core.Bucket) (core.Principal, error) {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if header == "" {
		return core.Principal{Kind: core.PrincipalAnonymous}, nil
	}
	scheme, token, _ := strings.Cut(header, " ")
	token = strings.TrimSpace(token)
	switch {
	case token == "":
		return core.Principal{}, errUnauthenticated
	case strings.EqualFold(scheme, "Firebase"):
		return a.user(ctx, b, token)
	case strings.EqualFold(scheme, "Bearer"):
		return a.admin(ctx, b, token)
	}
	return core.Principal{}, errUnauthenticated
}

// user verifies a user's ID token for the bucket's project.
func (a *API) user(ctx context.Context, b core.Bucket, token string) (core.Principal, error) {
	if a.opts.IDTokens == nil {
		return core.Principal{}, errUnauthenticated
	}
	c, err := a.opts.IDTokens.Verify(ctx, b.Project, token)
	switch {
	case idtoken.Status(err) != "":
		return core.Principal{}, errUnauthenticated
	case err != nil:
		a.log.Warn("storage: verifying an ID token", "bucket", b.Name, "error", err)
		return core.Principal{}, core.ErrUnavailable
	}
	return core.Principal{Kind: core.PrincipalUser, UID: c.UID, Token: c.Token, AuthTime: c.AuthTime, IssuedAt: c.IssuedAt}, nil
}

// admin verifies an admin credential and that it may act on the bucket's project: a service
// account of that project, or an account that owns it.
//
// A service account is named for the audit log. An account's CLI token is the account itself, so
// it is ConsoleAccount: an upload it starts is checked again at finalize, and refused once the
// account no longer owns the project (ADR-022 §15).
func (a *API) admin(ctx context.Context, b core.Bucket, token string) (core.Principal, error) {
	if a.opts.Credentials == nil || a.opts.Projects == nil {
		return core.Principal{}, errUnauthenticated
	}
	var (
		p   *authn.Principal
		err error
	)
	switch {
	case strings.HasPrefix(token, authn.ServiceTokenPrefix):
		p, err = a.opts.Credentials.VerifyServiceToken(ctx, token)
	case strings.HasPrefix(token, authn.AccessTokenPrefix):
		p, err = a.opts.Credentials.VerifyAccessToken(ctx, token)
	default:
		return core.Principal{}, errUnauthenticated
	}
	if err == nil {
		got, gerr := a.opts.Projects.GetProject(ctx, p, strconv.FormatInt(b.Project, 10))
		switch {
		case gerr != nil:
			err = gerr
		case got.Number != b.Project || got.State != "ACTIVE":
			err = apierr.New(apierr.NotFound, "project not found")
		}
	}
	if err != nil {
		if refused(err) {
			a.log.Info("storage: admin credential refused", "bucket", b.Name, "error", err)
			return core.Principal{}, errUnauthenticated
		}
		a.log.Warn("storage: verifying an admin credential", "bucket", b.Name, "error", err)
		return core.Principal{}, core.ErrUnavailable
	}
	admin := core.Principal{Kind: core.PrincipalAdmin}
	switch p.Kind {
	case authn.KindServiceAccount:
		admin.ServiceAccount = p.Email
	case authn.KindAccount:
		admin.ConsoleAccount = p.AccountID.String()
	}
	return admin, nil
}

// refused reports whether err says the credential is not accepted, rather than that it could
// not be checked.
func refused(err error) bool {
	ae, ok := errors.AsType[*apierr.Error](err)
	if !ok {
		return false
	}
	switch ae.Code {
	case apierr.Unauthenticated, apierr.PermissionDenied, apierr.NotFound:
		return true
	}
	return false
}
