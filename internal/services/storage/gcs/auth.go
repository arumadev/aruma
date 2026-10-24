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
// credential, bound at its start (routeUpload).
//
// The bucket is resolved before the credential is looked at, so a bucket that does not exist
// is 404 to anyone, as production answers an anonymous caller (bucket_probe.json); one that
// exists is 401 without a valid token, and 403 to another project's account.

// admit resolves a request's bucket and checks its caller may act on it with perm. It answers
// the refusal itself and reports false; otherwise it returns the bucket and the admin principal
// core acts as.
func (a *API) admit(w http.ResponseWriter, r *http.Request, bucket, perm string) (core.Bucket, core.Principal, bool) {
	b, err := a.svc.Bucket(r.Context(), bucket)
	if err != nil {
		a.writeError(w, err, bucket, "")
		return core.Bucket{}, core.Principal{}, false
	}
	sa, err := a.authenticate(r, perm)
	if err == nil {
		err = admitted(sa, b, perm)
	}
	if err != nil {
		a.writeError(w, err, bucket, "")
		return core.Bucket{}, core.Principal{}, false
	}
	return b, principal(sa), true
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
	return forbidden(sa, perm)
}

// forbidden is GCS's refusal of a caller without perm.
func forbidden(sa *authn.Principal, perm string) error {
	return &apiError{status: http.StatusForbidden, reason: wire.ReasonForbidden,
		message: sa.Email + " does not have " + perm + " access to the Google Cloud Storage " + noun(perm) +
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
