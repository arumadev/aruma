package core

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"github.com/arumadev/aruma/internal/platform/apierr"
)

// The data services (Auth from Phase 2 on) reach the control plane through these calls. They
// carry no principal: the caller is a client SDK identified by an API key, or a token signed
// by a service account.

// KeyProject is the project behind a client API key.
type KeyProject struct {
	Number int64
	ID     string
	Key    ResolvedKey
}

// ProjectForKey resolves a client API key to its active project. Unknown or disabled keys and
// deleted projects are refused alike.
func (s *Service) ProjectForKey(ctx context.Context, key string) (KeyProject, error) {
	rk, err := s.LookupAPIKey(ctx, key)
	if err != nil {
		return KeyProject{}, err
	}
	p, err := s.q.GetProject(ctx, rk.ProjectNumber)
	if err != nil || p.State != "ACTIVE" {
		return KeyProject{}, apierr.Invalid("key", "API key not valid. Please pass a valid API key.")
	}
	return KeyProject{Number: p.ProjectNumber, ID: p.ProjectID, Key: rk}, nil
}

// ServiceAccountAssertion is a verified JWT signed by a service account key.
type ServiceAccountAssertion struct {
	Email   string
	Payload []byte
}

// VerifyServiceAccountJWT checks an RS256 JWT signed by an active key of an enabled service
// account that belongs to project, as custom tokens are. It verifies the signature only;
// the caller checks the claims. A token from another project's account is ErrOtherProject.
func (s *Service) VerifyServiceAccountJWT(ctx context.Context, project int64, token string) (ServiceAccountAssertion, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ServiceAccountAssertion{}, ErrBadAssertion
	}
	var h jwtHeader
	var c struct {
		Iss string `json:"iss"`
	}
	if decodeSegment(parts[0], &h) != nil || h.Alg != "RS256" || decodeSegment(parts[1], &c) != nil || c.Iss == "" {
		return ServiceAccountAssertion{}, ErrBadAssertion
	}
	keys, err := s.q.ListActiveKeysByEmail(ctx, c.Iss)
	if err != nil {
		return ServiceAccountAssertion{}, internal(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return ServiceAccountAssertion{}, ErrBadAssertion
	}
	if _, ok := verifyWithAny(keys, h.Kid, digest[:], sig); !ok {
		return ServiceAccountAssertion{}, ErrBadAssertion
	}
	sa, err := s.q.GetServiceAccountByEmail(ctx, c.Iss)
	if err != nil {
		return ServiceAccountAssertion{}, ErrBadAssertion
	}
	if sa.Disabled || sa.ProjectState != "ACTIVE" {
		return ServiceAccountAssertion{}, ErrBadAssertion
	}
	if sa.ProjectNumber != project {
		return ServiceAccountAssertion{}, ErrOtherProject
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	return ServiceAccountAssertion{Email: sa.Email, Payload: payload}, nil
}

// Errors of VerifyServiceAccountJWT.
var (
	ErrBadAssertion = apierr.New(apierr.Unauthenticated, "The token is not signed by an active service account key.")
	ErrOtherProject = apierr.New(apierr.PermissionDenied, "The token belongs to another project.")
)

// ProjectID returns the id of an active project by number, for the data services' token
// issuer and audience.
//
// A missing or inactive project is NOT_FOUND; a database failure is INTERNAL, as in
// ProjectNumber, so a token is not refused as foreign during an outage.
func (s *Service) ProjectID(ctx context.Context, number int64) (string, error) {
	p, err := s.q.GetProject(ctx, number)
	switch {
	case err != nil && !notFound(err):
		return "", internal(err)
	case err != nil || p.State != "ACTIVE":
		return "", errNotFound()
	}
	return p.ProjectID, nil
}

// ProjectNumber returns the number of an active project by id, for the public token discovery
// endpoints, which name the project as their issuer does.
//
// A missing or inactive project is NOT_FOUND; a database failure is INTERNAL, so callers do
// not mistake an outage for a missing project.
func (s *Service) ProjectNumber(ctx context.Context, id string) (int64, error) {
	p, err := s.q.GetProjectByID(ctx, id)
	switch {
	case err != nil && !notFound(err):
		return 0, internal(err)
	case err != nil || p.State != "ACTIVE":
		return 0, errNotFound()
	}
	return p.ProjectNumber, nil
}
