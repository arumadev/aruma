package core

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/platform/apierr"
	"github.com/osama300/aurma-project/internal/platform/authn"
	"github.com/osama300/aurma-project/internal/platform/ids"
	"github.com/osama300/aurma-project/internal/store"
	"github.com/osama300/aurma-project/internal/tenancy/queries"
)

// MaxServiceAccountKeys caps keys per service account (proposed in the Phase 1 plan).
const MaxServiceAccountKeys = 10

// ServiceAccount is a non-human identity for a developer's server, scoped to one project.
type ServiceAccount struct {
	ID            uuid.UUID
	ProjectNumber int64
	ProjectID     string
	Email         string
	UniqueID      string
	DisplayName   string
	Disabled      bool
	CreatedAt     time.Time
}

func toServiceAccount(s queries.ControlServiceAccount, projectID string) ServiceAccount {
	return ServiceAccount{ID: s.ID, ProjectNumber: s.ProjectNumber, ProjectID: projectID, Email: s.Email, UniqueID: s.UniqueID, DisplayName: s.DisplayName, Disabled: s.Disabled, CreatedAt: s.CreatedAt}
}

// ServiceAccountKey is a key's public record.
type ServiceAccountKey struct {
	KeyID       string
	Algorithm   string
	ValidAfter  time.Time
	ValidBefore *time.Time
	Disabled    bool
	CreatedAt   time.Time
}

func toServiceAccountKey(k queries.ControlServiceAccountKey) ServiceAccountKey {
	return ServiceAccountKey{KeyID: k.KeyID, Algorithm: k.Algorithm, ValidAfter: k.ValidAfter, ValidBefore: k.ValidBefore, Disabled: k.Disabled, CreatedAt: k.CreatedAt}
}

// GeneratedKey is a new key with its private half. It exists only in the response that
// creates it; Aruma keeps the certificate and forgets the private key.
type GeneratedKey struct {
	ServiceAccount ServiceAccount
	Key            ServiceAccountKey
	PrivateKeyPEM  string // PKCS#8, "BEGIN PRIVATE KEY"
}

// ServiceAccountEmail builds "<name>@<project_id>.sa.<project_domain>" (ADR-007).
func (s *Service) ServiceAccountEmail(name, projectID string) string {
	return name + "@" + projectID + ".sa." + s.opts.ProjectDomain
}

// CreateServiceAccount creates a service account. name becomes the local part of its email.
func (s *Service) CreateServiceAccount(ctx context.Context, p *authn.Principal, projectRef, name, displayName string) (ServiceAccount, error) {
	if err := requireAccount(p); err != nil {
		return ServiceAccount{}, err
	}
	if !ids.ValidServiceAccountName(name) {
		return ServiceAccount{}, apierr.Invalid("accountId", "Account ID must be 6 to 30 lowercase letters, digits or hyphens, start with a letter and not end with a hyphen.")
	}
	dn, err := validDisplayName(displayName, false)
	if err != nil {
		return ServiceAccount{}, err
	}
	var out ServiceAccount
	err = s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, err := s.resolveProject(ctx, q, p, projectRef, false)
		if err != nil {
			return err
		}
		sa, err := q.CreateServiceAccount(ctx, queries.CreateServiceAccountParams{
			ProjectNumber: proj.ProjectNumber, Email: s.ServiceAccountEmail(name, proj.ProjectID),
			UniqueID: ids.ServiceAccountUniqueID(), DisplayName: dn,
		})
		if err != nil {
			return err
		}
		out = toServiceAccount(sa, proj.ProjectID)
		return s.audit(ctx, q, p, proj.ProjectNumber, "serviceaccount.create", sa.Email, nil)
	})
	if c, ok := store.UniqueViolation(err); ok && c == "service_accounts_email_key" {
		return ServiceAccount{}, apierr.New(apierr.AlreadyExists, "A service account named %q already exists in this project.", name)
	}
	return out, internal(err)
}

// ListServiceAccounts lists a project's service accounts.
func (s *Service) ListServiceAccounts(ctx context.Context, p *authn.Principal, projectRef string) ([]ServiceAccount, error) {
	proj, err := s.resolveProject(ctx, s.q, p, projectRef, false)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListServiceAccounts(ctx, proj.ProjectNumber)
	if err != nil {
		return nil, internal(err)
	}
	out := make([]ServiceAccount, 0, len(rows))
	for _, r := range rows {
		out = append(out, toServiceAccount(r, proj.ProjectID))
	}
	return out, nil
}

func (s *Service) resolveServiceAccount(ctx context.Context, q *queries.Queries, p *authn.Principal, projectRef string, id uuid.UUID) (queries.ControlProject, queries.ControlServiceAccount, error) {
	proj, err := s.resolveProject(ctx, q, p, projectRef, false)
	if err != nil {
		return proj, queries.ControlServiceAccount{}, err
	}
	sa, err := q.GetServiceAccount(ctx, queries.GetServiceAccountParams{ProjectNumber: proj.ProjectNumber, ID: id})
	if err != nil {
		if notFound(err) {
			return proj, sa, errNotFound()
		}
		return proj, sa, internal(err)
	}
	return proj, sa, nil
}

// DeleteServiceAccount deletes a service account, its keys and its tokens.
func (s *Service) DeleteServiceAccount(ctx context.Context, p *authn.Principal, projectRef string, id uuid.UUID) error {
	if err := requireAccount(p); err != nil {
		return err
	}
	return internal(s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, sa, err := s.resolveServiceAccount(ctx, q, p, projectRef, id)
		if err != nil {
			return err
		}
		if _, err := q.DeleteServiceAccount(ctx, queries.DeleteServiceAccountParams{ProjectNumber: proj.ProjectNumber, ID: id}); err != nil {
			return err
		}
		return s.audit(ctx, q, p, proj.ProjectNumber, "serviceaccount.delete", sa.Email, nil)
	}))
}

// ListServiceAccountKeys lists a service account's keys (public records only).
func (s *Service) ListServiceAccountKeys(ctx context.Context, p *authn.Principal, projectRef string, id uuid.UUID) ([]ServiceAccountKey, error) {
	proj, _, err := s.resolveServiceAccount(ctx, s.q, p, projectRef, id)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListServiceAccountKeys(ctx, queries.ListServiceAccountKeysParams{ProjectNumber: proj.ProjectNumber, ServiceAccountID: id})
	if err != nil {
		return nil, internal(err)
	}
	out := make([]ServiceAccountKey, 0, len(rows))
	for _, r := range rows {
		out = append(out, toServiceAccountKey(r))
	}
	return out, nil
}

// noExpiry is the validity end Google gives user-managed keys.
var noExpiry = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)

// CreateServiceAccountKey generates an RSA-2048 key pair. The private key is returned once and
// never stored (ADR-010).
func (s *Service) CreateServiceAccountKey(ctx context.Context, p *authn.Principal, projectRef string, id uuid.UUID) (GeneratedKey, error) {
	if err := requireAccount(p); err != nil {
		return GeneratedKey{}, err
	}
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return GeneratedKey{}, internal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return GeneratedKey{}, internal(err)
	}
	var out GeneratedKey
	err = s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, sa, err := s.resolveServiceAccount(ctx, q, p, projectRef, id)
		if err != nil {
			return err
		}
		n, err := q.CountServiceAccountKeys(ctx, queries.CountServiceAccountKeysParams{ProjectNumber: proj.ProjectNumber, ServiceAccountID: id})
		if err != nil {
			return err
		}
		if n >= MaxServiceAccountKeys {
			return apierr.New(apierr.FailedPrecondition, "A service account can have at most %d keys. Delete one first.", MaxServiceAccountKeys)
		}
		now := time.Now().UTC().Truncate(time.Second)
		certPEM, err := selfSignedCert(priv, sa.Email, now)
		if err != nil {
			return err
		}
		key, err := q.CreateServiceAccountKey(ctx, queries.CreateServiceAccountKeyParams{
			KeyID: ids.KeyID(), CertificatePem: certPEM, ValidAfter: now, ValidBefore: nil,
			ProjectNumber: proj.ProjectNumber, ServiceAccountID: id,
		})
		if err != nil {
			return err
		}
		out = GeneratedKey{
			ServiceAccount: toServiceAccount(sa, proj.ProjectID),
			Key:            toServiceAccountKey(key),
			PrivateKeyPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})),
		}
		return s.audit(ctx, q, p, proj.ProjectNumber, "serviceaccount.key.create", sa.Email, map[string]any{"keyId": key.KeyID})
	})
	return out, internal(err)
}

// DeleteServiceAccountKey deletes a key; assertions signed with it stop working at once.
func (s *Service) DeleteServiceAccountKey(ctx context.Context, p *authn.Principal, projectRef string, id uuid.UUID, keyID string) error {
	if err := requireAccount(p); err != nil {
		return err
	}
	return internal(s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		proj, sa, err := s.resolveServiceAccount(ctx, q, p, projectRef, id)
		if err != nil {
			return err
		}
		n, err := q.DeleteServiceAccountKey(ctx, queries.DeleteServiceAccountKeyParams{ProjectNumber: proj.ProjectNumber, ServiceAccountID: id, KeyID: keyID})
		if err != nil {
			return err
		}
		if n == 0 {
			return errNotFound()
		}
		return s.audit(ctx, q, p, proj.ProjectNumber, "serviceaccount.key.delete", sa.Email, map[string]any{"keyId": keyID})
	}))
}

// PublicCertificates returns a service account's active certificates by key id, served at the
// key file's client_x509_cert_url. Certificates are public.
func (s *Service) PublicCertificates(ctx context.Context, email string) (map[string]string, error) {
	rows, err := s.q.ListActiveKeysByEmail(ctx, email)
	if err != nil {
		return nil, internal(err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.KeyID] = r.CertificatePem
	}
	return out, nil
}

func selfSignedCert(priv *rsa.PrivateKey, email string, now time.Time) (string, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return "", err
	}
	cn := email
	if len(cn) > 64 { // X.520 limit on common names
		cn = cn[:strings.IndexByte(cn, '@')]
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              noExpiry,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		EmailAddresses:        []string{email},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
}
