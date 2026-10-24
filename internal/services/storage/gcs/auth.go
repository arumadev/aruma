package gcs

import (
	"net/http"
	"strings"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/services/storage/core"
	"github.com/arumadev/aruma/internal/services/storage/wire"
)

// Who may call the GCS surface (ADR-022 §14): a service account of the bucket's project, with
// the one-hour access token Aruma's OAuth endpoint issued it (aruma_sat_...), as the Admin SDKs
// send it in Authorization: Bearer. Nothing else: no anonymous caller (the emulator mode of Node
// and Go sends none, and accepting it would be more permissive than Firebase, E §9), no ID
// token, no personal CLI token. An upload session is the one exception: its upload_id is the
// credential, bound at its start.
//
// The bucket is resolved before the credential is looked at, so a bucket that does not exist
// is 404 to anyone, as production answers an anonymous caller (bucket_probe.json); one that
// exists is 401 without a valid token, and 403 to another project's account. That 403 is
// storage's exception to ADR-010 S-11 (ADR-022 §14): a bucket's existence is no secret (its
// name is the project id every app's config carries), and the refusal comes before any object
// is looked up, so it says nothing of what the bucket holds.

// admit checks that a request's caller may act on a resolved bucket with perm, and returns the
// admin principal core acts as.
func (a *API) admit(r *http.Request, b core.Bucket, perm string) (core.Principal, error) {
	sa, err := a.authenticate(r, perm)
	if err == nil {
		err = admitted(sa, b, perm)
	}
	if err != nil {
		return core.Principal{}, err
	}
	return principal(sa), nil
}

// authenticate returns the service account a request's Bearer token belongs to.
func (a *API) authenticate(r *http.Request, perm string) (*authn.Principal, error) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return nil, &apiError{status: http.StatusUnauthorized, reason: wire.ReasonRequired,
			message: "Anonymous caller does not have " + perm + " access to the Google Cloud Storage " + noun(perm) +
				". Permission '" + perm + "' denied on resource (or it may not exist)."}
	}
	scheme, token, _ := strings.Cut(h, " ")
	token = strings.TrimSpace(token)
	if !strings.EqualFold(scheme, "Bearer") || !strings.HasPrefix(token, authn.ServiceTokenPrefix) || a.opts.Credentials == nil {
		return nil, errInvalidCredentials
	}
	sa, err := a.opts.Credentials.VerifyServiceToken(r.Context(), token)
	switch apierr.CodeOf(err) {
	case apierr.OK:
	case apierr.Unauthenticated, apierr.PermissionDenied, apierr.NotFound, apierr.InvalidArgument:
		return nil, errInvalidCredentials
	default:
		a.log.Error("gcs: verify a service account token", "error", err)
		return nil, core.ErrUnavailable
	}
	if sa == nil || sa.Kind != authn.KindServiceAccount {
		return nil, errInvalidCredentials
	}
	return sa, nil
}

// admitted checks that a service account belongs to the bucket's project: another project's is
// 403, in GCS's words.
func admitted(sa *authn.Principal, b core.Bucket, perm string) error {
	if sa.ProjectNumber == b.Project {
		return nil
	}
	return forbidden(sa.Email, perm)
}

// forbidden is GCS's refusal of a caller without perm.
func forbidden(email, perm string) error {
	return &apiError{status: http.StatusForbidden, reason: wire.ReasonForbidden,
		message: email + " does not have " + perm + " access to the Google Cloud Storage " + noun(perm) +
			". Permission '" + perm + "' denied on resource (or it may not exist)."}
}

// noun is what a permission is on: a bucket or an object.
func noun(perm string) string {
	if strings.HasPrefix(perm, "storage.buckets.") {
		return "bucket"
	}
	return "object"
}

// principal is the admin core acts as for a service account: it bypasses the rules, as the
// Admin SDKs do in Firebase (ADR-021 §7).
func principal(sa *authn.Principal) core.Principal {
	return core.Principal{Kind: core.PrincipalAdmin, ServiceAccount: sa.Email}
}

// errInvalidCredentials is GCS's answer to a token it does not accept.
var errInvalidCredentials = &apiError{status: http.StatusUnauthorized, reason: "authError", message: "Invalid Credentials"}
