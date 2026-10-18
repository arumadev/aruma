package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/osama300/aurma-project/internal/identity/queries"
	"github.com/osama300/aurma-project/internal/platform/apierr"
)

// Sign-in with Google and Apple (T15, T20): per-project provider settings and where the
// providers are reached. The flow is in idp_flow.go, token checks in idp_keys.go and the
// account rules in idp_signin.go; docs/research/phase2-idp.md records the decisions.

// Provider ids.
const (
	GoogleProvider = "google.com"
	AppleProvider  = "apple.com"
)

// IdPEndpoints are where a provider is reached and the issuers its ID tokens carry.
type IdPEndpoints struct {
	AuthURL   string
	TokenURL  string
	JWKSURL   string
	RevokeURL string
	Issuers   []string
}

// The providers' published endpoints.
var (
	GoogleEndpoints = IdPEndpoints{
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		JWKSURL:  "https://www.googleapis.com/oauth2/v3/certs",
		Issuers:  []string{"https://accounts.google.com", "accounts.google.com"},
	}
	AppleEndpoints = IdPEndpoints{
		AuthURL:   "https://appleid.apple.com/auth/authorize",
		TokenURL:  "https://appleid.apple.com/auth/token",
		JWKSURL:   "https://appleid.apple.com/auth/keys",
		RevokeURL: "https://appleid.apple.com/auth/revoke",
		Issuers:   []string{"https://appleid.apple.com"},
	}
)

// idpRuntime holds the provider endpoints, the HTTP client that calls them and the cached
// provider keys.
type idpRuntime struct {
	mu        sync.RWMutex
	endpoints map[string]IdPEndpoints
	client    *http.Client
	keys      *jwksCache
}

func newIdPRuntime() *idpRuntime {
	client := &http.Client{Timeout: 10 * time.Second}
	return &idpRuntime{
		endpoints: map[string]IdPEndpoints{GoogleProvider: GoogleEndpoints, AppleProvider: AppleEndpoints},
		client:    client,
		keys:      newJWKSCache(client),
	}
}

// SetIdPEndpoints points the providers somewhere else. Only tests call it, through an app
// option, to stand in fake providers; no configuration file or request can reach it.
func (s *Service) SetIdPEndpoints(google, apple IdPEndpoints, client *http.Client) {
	s.idp.mu.Lock()
	defer s.idp.mu.Unlock()
	s.idp.endpoints = map[string]IdPEndpoints{GoogleProvider: google, AppleProvider: apple}
	s.idp.client = client
	s.idp.keys = newJWKSCache(client)
}

func (s *Service) idpEndpoints(provider string) (IdPEndpoints, *http.Client, *jwksCache) {
	s.idp.mu.RLock()
	defer s.idp.mu.RUnlock()
	return s.idp.endpoints[provider], s.idp.client, s.idp.keys
}

// IdPConfig is a provider's settings as the console shows them: secrets only as "set".
type IdPConfig struct {
	ProviderID      string
	Enabled         bool
	ClientID        string // Google web client ID, or Apple Services ID
	ClientSecretSet bool   // Google
	TeamID          string // Apple
	KeyID           string // Apple
	PrivateKeySet   bool   // Apple
}

// IdPConfigUpdate changes a provider's settings; nil fields stay as they are, and an empty
// secret clears it.
type IdPConfigUpdate struct {
	Enabled      *bool
	ClientID     *string
	ClientSecret *string
	TeamID       *string
	KeyID        *string
	PrivateKey   *string
}

func knownProvider(id string) bool { return id == GoogleProvider || id == AppleProvider }

var errUnknownProvider = apierr.New(apierr.NotFound, "Unknown sign-in provider.")

func toIdPConfig(c queries.AuthIdpConfig) IdPConfig {
	return IdPConfig{
		ProviderID: c.ProviderID, Enabled: c.Enabled, ClientID: c.ClientID, ClientSecretSet: c.ClientSecretEnc != nil,
		TeamID: c.AppleTeamID, KeyID: c.AppleKeyID, PrivateKeySet: c.ApplePrivateKeyEnc != nil,
	}
}

// IdPConfigs returns the settings of Google and Apple, in that order, unconfigured ones as
// disabled.
func (s *Service) IdPConfigs(ctx context.Context, p Project) ([]IdPConfig, error) {
	rows, err := s.q.ListIdPConfigs(ctx, p.Number)
	if err != nil {
		return nil, err
	}
	out := []IdPConfig{{ProviderID: GoogleProvider}, {ProviderID: AppleProvider}}
	for _, r := range rows {
		for i := range out {
			if out[i].ProviderID == r.ProviderID {
				out[i] = toIdPConfig(r)
			}
		}
	}
	return out, nil
}

func (s *Service) idpConfigRow(ctx context.Context, q *queries.Queries, p Project, provider string) (queries.AuthIdpConfig, error) {
	c, err := q.GetIdPConfig(ctx, queries.GetIdPConfigParams{ProjectNumber: p.Number, ProviderID: provider})
	if errors.Is(err, pgx.ErrNoRows) {
		return queries.AuthIdpConfig{ProjectNumber: p.Number, ProviderID: provider}, nil
	}
	return c, err
}

// The AAD binds each sealed secret to its row and column (ADR-010).
func idpSecretAAD(p Project, provider, column string) []byte {
	return []byte("auth.idp_configs|" + column + "|" + strconv.FormatInt(p.Number, 10) + "/" + provider)
}

var (
	appleIDRe  = regexp.MustCompile(`^[A-Z0-9]{10}$`)
	clientIDRe = regexp.MustCompile(`^[A-Za-z0-9._\-]{1,255}$`)
)

func invalidIdPConfig(detail string) error { return failf("INVALID_CONFIG", detail) }

// UpdateIdPConfig changes a provider's settings. A provider can be enabled only when it has
// everything its flow needs; the Apple key must be a PEM PKCS#8 P-256 key, as Apple issues.
func (s *Service) UpdateIdPConfig(ctx context.Context, p Project, provider string, u IdPConfigUpdate) (IdPConfig, error) {
	if !knownProvider(provider) {
		return IdPConfig{}, errUnknownProvider
	}
	var out IdPConfig
	err := s.tx(ctx, func(q *queries.Queries, _ pgx.Tx) error {
		c, err := s.idpConfigRow(ctx, q, p, provider)
		if err != nil {
			return err
		}
		if u.Enabled != nil {
			c.Enabled = *u.Enabled
		}
		if u.ClientID != nil {
			id := strings.TrimSpace(*u.ClientID)
			if id != "" && !clientIDRe.MatchString(id) {
				return invalidIdPConfig("client ID has unexpected characters")
			}
			c.ClientID = id
		}
		if provider == GoogleProvider {
			if u.TeamID != nil || u.KeyID != nil || u.PrivateKey != nil {
				return invalidIdPConfig("teamId, keyId and privateKey apply to Apple only")
			}
			if u.ClientSecret != nil {
				if c.ClientSecretEnc, err = s.sealOptional(*u.ClientSecret, idpSecretAAD(p, provider, "client_secret_enc")); err != nil {
					return err
				}
			}
			if c.Enabled && (c.ClientID == "" || c.ClientSecretEnc == nil) {
				return invalidIdPConfig("Google needs a client ID and secret before it can be enabled")
			}
		} else {
			if u.ClientSecret != nil {
				return invalidIdPConfig("Apple signs its client secret with the private key; set privateKey instead")
			}
			for _, f := range []struct {
				in  *string
				dst *string
				nm  string
			}{{u.TeamID, &c.AppleTeamID, "team ID"}, {u.KeyID, &c.AppleKeyID, "key ID"}} {
				if f.in == nil {
					continue
				}
				v := strings.TrimSpace(*f.in)
				if v != "" && !appleIDRe.MatchString(v) {
					return invalidIdPConfig("Apple " + f.nm + " must be 10 characters, A-Z and 0-9")
				}
				*f.dst = v
			}
			if u.PrivateKey != nil {
				if strings.TrimSpace(*u.PrivateKey) != "" {
					if _, err := parseAppleKey([]byte(*u.PrivateKey)); err != nil {
						return invalidIdPConfig("private key must be the PEM .p8 key Apple issues (PKCS#8, P-256)")
					}
				}
				if c.ApplePrivateKeyEnc, err = s.sealOptional(strings.TrimSpace(*u.PrivateKey), idpSecretAAD(p, provider, "apple_private_key_enc")); err != nil {
					return err
				}
			}
			if c.Enabled && (c.ClientID == "" || c.AppleTeamID == "" || c.AppleKeyID == "" || c.ApplePrivateKeyEnc == nil) {
				return invalidIdPConfig("Apple needs a Services ID, team ID, key ID and private key before it can be enabled")
			}
		}
		row, err := q.UpsertIdPConfig(ctx, queries.UpsertIdPConfigParams{
			ProjectNumber: p.Number, ProviderID: provider, Enabled: c.Enabled, ClientID: c.ClientID,
			ClientSecretEnc: c.ClientSecretEnc, AppleTeamID: c.AppleTeamID, AppleKeyID: c.AppleKeyID,
			ApplePrivateKeyEnc: c.ApplePrivateKeyEnc,
		})
		if err != nil {
			return err
		}
		out = toIdPConfig(row)
		s.audit(ctx, q, p, "", "config.idp", "ok", map[string]any{
			"provider": provider, "enabled": row.Enabled, "secretChanged": u.ClientSecret != nil || u.PrivateKey != nil,
		})
		return nil
	})
	return out, err
}

// sealOptional encrypts a secret, or returns nil for an empty one.
func (s *Service) sealOptional(secret string, aad []byte) ([]byte, error) {
	if secret == "" {
		return nil, nil
	}
	return s.keys.Seal([]byte(secret), aad)
}

func parseAppleKey(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("not a PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, errors.New("not a P-256 key")
	}
	return ec, nil
}

// idpCreds is an enabled provider ready for a sign-in.
type idpCreds struct {
	provider  string
	clientID  string
	secret    string            // Google client secret
	teamID    string            // Apple
	keyID     string            // Apple
	appleKey  *ecdsa.PrivateKey // Apple
	endpoints IdPEndpoints
	client    *http.Client
	keys      *jwksCache
}

// enabledIdP loads a provider for a sign-in; a disabled or unknown one is OPERATION_NOT_ALLOWED,
// as Firebase answers for a provider that is off.
func (s *Service) enabledIdP(ctx context.Context, p Project, provider string) (*idpCreds, error) {
	return s.loadIdP(ctx, p, provider, true)
}

// loadIdP loads a provider's credentials; revoking tokens on deletion does not need it enabled.
func (s *Service) loadIdP(ctx context.Context, p Project, provider string, requireEnabled bool) (*idpCreds, error) {
	if !knownProvider(provider) {
		return nil, failf("INVALID_PROVIDER_ID", "Aruma supports google.com and apple.com")
	}
	c, err := s.idpConfigRow(ctx, s.q, p, provider)
	if err != nil {
		return nil, err
	}
	if requireEnabled && !c.Enabled {
		return nil, ErrOperationNotAllowed
	}
	ep, client, keys := s.idpEndpoints(provider)
	out := &idpCreds{provider: provider, clientID: c.ClientID, teamID: c.AppleTeamID, keyID: c.AppleKeyID, endpoints: ep, client: client, keys: keys}
	switch provider {
	case GoogleProvider:
		b, err := s.keys.Open(c.ClientSecretEnc, idpSecretAAD(p, provider, "client_secret_enc"))
		if err != nil {
			return nil, err
		}
		out.secret = string(b)
	case AppleProvider:
		b, err := s.keys.Open(c.ApplePrivateKeyEnc, idpSecretAAD(p, provider, "apple_private_key_enc"))
		if err != nil {
			return nil, err
		}
		if out.appleKey, err = parseAppleKey(b); err != nil {
			return nil, err
		}
	}
	return out, nil
}
