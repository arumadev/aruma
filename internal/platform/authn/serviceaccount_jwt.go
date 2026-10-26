package authn

import (
	"cmp"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/arumadev/aruma/internal/platform/apierr"
)

// Self-signed service account JWTs (ADR-029 §3). The Admin SDKs sign their own RS256 JWT with
// the key file's private key and send it to the Document Database on every RPC, without ever
// contacting token_uri: Go with kid = private_key_id and aud = "", Node with neither. The
// rules here are those of tenancy/core's ExchangeAssertion (clockSkew, maxAssertionLifetime,
// verifyWithAny), except the audience, which names the data plane rather than the token
// endpoint, so neither kind of assertion is accepted where the other is.

// OAuth scopes and the audience the Document Database accepts.
const (
	ScopeDatastore     = "https://www.googleapis.com/auth/datastore"
	ScopeCloudPlatform = "https://www.googleapis.com/auth/cloud-platform"
	FirestoreAudience  = "https://firestore.googleapis.com/"
)

const (
	// jwtClockSkew and maxJWTLifetime are tenancy/core's clockSkew and maxAssertionLifetime.
	jwtClockSkew   = 60 * time.Second
	maxJWTLifetime = time.Hour

	// VerifiedTokenTTL bounds how long a verified token is trusted without looking at its
	// account and key again, so a deleted key or a disabled account stops working within it
	// (ADR-029 §3.8). Streams re-check at the same interval.
	VerifiedTokenTTL = 60 * time.Second

	// maxCachedJWTs bounds the cache. Only verified tokens enter it, so filling it takes valid
	// keys; past it, tokens are verified on every call instead.
	maxCachedJWTs = 10_000
)

// ServiceAccountPublicKey is an active key of a service account: not disabled, and inside its
// valid_after and valid_before.
type ServiceAccountPublicKey struct {
	KeyID          string // private_key_id in the key file, control.service_account_keys.key_id
	CertificatePEM string // the self-signed X.509 certificate stored for it
}

// ServiceAccountRecord is what a self-signed JWT is checked against.
type ServiceAccountRecord struct {
	ID            uuid.UUID
	Email         string
	ProjectNumber int64
	// Usable is false when the account is disabled or its project is not ACTIVE.
	Usable bool
	// Keys are the account's active keys, in the order verifyWithAny tries them.
	Keys []ServiceAccountPublicKey
}

// ServiceAccountDirectory looks up service accounts by email. tenancy/core implements it with
// ListActiveKeysByEmail and GetServiceAccountByEmail. An unknown email is a record without
// keys and a nil error; an error means the lookup itself failed.
type ServiceAccountDirectory interface {
	LookupServiceAccount(ctx context.Context, email string) (ServiceAccountRecord, error)
}

// ServiceAccountJWTVerifier verifies self-signed service account JWTs and caches the verified
// ones for at most VerifiedTokenTTL. It is safe for concurrent use.
type ServiceAccountJWTVerifier struct {
	dir       ServiceAccountDirectory
	publicURL string // without a trailing slash
	log       *slog.Logger
	now       func() time.Time

	mu    sync.Mutex
	cache map[[sha256.Size]byte]cachedJWT
	// revocations counts the Forget calls. A verification that saw a revocation land while it
	// read the directory does not cache its result, so it cannot outlive the revocation.
	revocations atomic.Uint64
}

type cachedJWT struct {
	p     Principal
	until time.Time
}

// NewServiceAccountJWTVerifier returns a verifier for tokens whose audience may also be
// publicURL, Aruma's public_url.
func NewServiceAccountJWTVerifier(dir ServiceAccountDirectory, publicURL string, log *slog.Logger) *ServiceAccountJWTVerifier {
	return &ServiceAccountJWTVerifier{
		dir:       dir,
		publicURL: strings.TrimSuffix(publicURL, "/"),
		log:       cmp.Or(log, slog.Default()),
		now:       time.Now,
		cache:     make(map[[sha256.Size]byte]cachedJWT),
	}
}

// errInvalidCredentials is Google's answer to a credential it does not accept.
var errInvalidCredentials = apierr.New(apierr.Unauthenticated,
	"Request had invalid authentication credentials. Expected OAuth 2 access token, login cookie or other valid authentication credential. See https://developers.google.com/identity/sign-in/web/devconsole-project.")

// Verify checks a self-signed service account JWT by ADR-029 §3.1-3.5 and returns its service
// account, with ProjectNumber the only project it may touch (§3.6 is the caller's). Every
// refusal is UNAUTHENTICATED, which the SDKs answer by signing a new token; a failed lookup is
// returned as it came.
func (v *ServiceAccountJWTVerifier) Verify(ctx context.Context, token string) (*Principal, error) {
	return v.verify(ctx, token, true)
}

// Recheck verifies token again without the cache, as a stream does every VerifiedTokenTTL.
func (v *ServiceAccountJWTVerifier) Recheck(ctx context.Context, token string) (*Principal, error) {
	return v.verify(ctx, token, false)
}

func (v *ServiceAccountJWTVerifier) verify(ctx context.Context, token string, cached bool) (*Principal, error) {
	now := v.now()
	sum := sha256.Sum256([]byte(token))
	if cached {
		if p, ok := v.lookup(sum, now); ok {
			return p, nil
		}
	}
	gen := v.revocations.Load()
	p, err := v.verifyUncached(ctx, token, now)
	if err != nil {
		v.forget(func(k [sha256.Size]byte, _ cachedJWT) bool { return k == sum })
		return nil, err
	}
	v.store(sum, p, now, gen)
	return clonePrincipal(p), nil
}

type saJWTHeader struct {
	Alg string  `json:"alg"`
	Kid string  `json:"kid"`
	Typ *string `json:"typ"`
}

type saJWTClaims struct {
	Iss   string          `json:"iss"`
	Sub   string          `json:"sub"`
	Aud   json.RawMessage `json:"aud"`
	Scope *string         `json:"scope"`
	Iat   float64         `json:"iat"`
	Exp   float64         `json:"exp"`
}

func (v *ServiceAccountJWTVerifier) verifyUncached(ctx context.Context, token string, now time.Time) (*Principal, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, v.refuse("not a JWT")
	}
	// §3.1: RS256 exactly. Never none (logged, ADR-010 S-3), never HMAC with a public key as
	// the secret.
	var h saJWTHeader
	if decodeJWTSegment(parts[0], &h) != nil {
		return nil, v.refuse("malformed header")
	}
	if h.Alg != "RS256" {
		v.log.Warn("rejected service account JWT with unsupported alg", "alg", clipValue(h.Alg, 16))
		return nil, errInvalidCredentials
	}
	if h.Typ != nil && *h.Typ != "JWT" {
		return nil, v.refuse("typ is not JWT")
	}
	var c saJWTClaims
	if decodeJWTSegment(parts[1], &c) != nil {
		return nil, v.refuse("malformed claims")
	}
	// §3.2: iss is the account, and sub the same.
	if c.Iss == "" || c.Sub != c.Iss {
		return nil, v.refuse("iss is empty or sub differs from it")
	}
	// §3.4: a scope naming the Document Database; a token without one is refused.
	if c.Scope == nil || !hasDocumentsScope(strings.Fields(*c.Scope)) {
		return nil, v.refuse("scope names neither datastore nor cloud-platform")
	}
	if !v.audienceAccepted(c.Aud) {
		return nil, v.refuse("aud is not the Document Database")
	}
	// §3.5.
	iat, exp := unixTime(c.Iat), unixTime(c.Exp)
	switch {
	case !plausibleTime(c.Iat) || !plausibleTime(c.Exp):
		return nil, v.refuse("iat or exp is not a time")
	case iat.After(now.Add(jwtClockSkew)):
		return nil, v.refuse("iat is in the future")
	case !exp.After(now.Add(-jwtClockSkew)):
		return nil, v.refuse("expired")
	case !exp.After(iat) || exp.Sub(iat) > maxJWTLifetime+jwtClockSkew:
		return nil, v.refuse("lifetime above one hour")
	}

	// §3.3: the signature is checked before anything about the account counts, so an unknown,
	// disabled or deleted account all look the same without its key.
	sig, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[2], "="))
	if err != nil || len(sig) == 0 {
		return nil, v.refuse("malformed signature")
	}
	sa, err := v.dir.LookupServiceAccount(ctx, c.Iss)
	if err != nil {
		return nil, apierr.From(err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	keyID, ok := verifyRS256WithAny(sa.Keys, h.Kid, digest[:], sig)
	if !ok {
		return nil, v.refuse("no active key of the account verifies the signature")
	}
	if !sa.Usable || sa.Email != c.Iss {
		return nil, v.refuse("the account is disabled or its project is not active")
	}
	return &Principal{
		Kind: KindServiceAccount, Method: MethodServiceAccountJWT,
		ServiceAccountID: sa.ID, ProjectNumber: sa.ProjectNumber, Email: sa.Email,
		KeyID: keyID, Scopes: strings.Fields(*c.Scope), ExpiresAt: exp,
	}, nil
}

// refuse logs why a token was refused, at debug level and without the token (ADR-010 S-15),
// and returns the one answer every refusal gets.
func (v *ServiceAccountJWTVerifier) refuse(reason string) error {
	v.log.Debug("refused service account JWT", "reason", reason)
	return errInvalidCredentials
}

// audienceAccepted applies §3.4: aud is absent or empty, Firestore's audience, or Aruma's
// public_url (one trailing slash ignored); an array must hold only those values. Anything
// else, the token endpoint's address above all, is refused, so an assertion written for
// /oauth2/v1/token cannot be replayed against the data plane.
func (v *ServiceAccountJWTVerifier) audienceAccepted(raw json.RawMessage) bool {
	ok := func(a string) bool {
		return a == "" || a == FirestoreAudience || (v.publicURL != "" && strings.TrimSuffix(a, "/") == v.publicURL)
	}
	if len(raw) == 0 || string(raw) == "null" {
		return true
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return ok(one)
	}
	var many []string
	if json.Unmarshal(raw, &many) == nil {
		return !slices.ContainsFunc(many, func(a string) bool { return !ok(a) })
	}
	return false
}

func hasDocumentsScope(scopes []string) bool {
	return slices.Contains(scopes, ScopeDatastore) || slices.Contains(scopes, ScopeCloudPlatform)
}

// verifyRS256WithAny is tenancy/core's verifyWithAny: the key kid names, and no other, or each
// active key in turn when the client sent no kid (firebase-admin never does). It returns the
// verifying key.
func verifyRS256WithAny(keys []ServiceAccountPublicKey, kid string, digest, sig []byte) (string, bool) {
	for _, k := range keys {
		if kid != "" && k.KeyID != kid {
			continue
		}
		block, _ := pem.Decode([]byte(k.CertificatePEM))
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		pub, ok := cert.PublicKey.(*rsa.PublicKey)
		if ok && rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest, sig) == nil {
			return k.KeyID, true
		}
	}
	return "", false
}

// CredentialRevocations hears of service account credentials that stop being valid, so a
// cache of verified tokens drops them at once rather than within VerifiedTokenTTL (ADR-029
// §3.8). tenancy/core calls it once the change has committed; *ServiceAccountJWTVerifier
// implements it, and internal/app connects the two (core.Service.OnServiceAccountRevoked).
// Each method must be quick and must not fail: it runs on the request that made the change.
type CredentialRevocations interface {
	// ForgetKey: the service account key keyID was deleted.
	ForgetKey(keyID string)
	// ForgetServiceAccount: the service account was deleted or disabled.
	ForgetServiceAccount(id uuid.UUID)
	// ForgetProject: the project was deleted, and with it every service account of it.
	ForgetProject(project int64)
}

var _ CredentialRevocations = (*ServiceAccountJWTVerifier)(nil)

// ForgetKey drops the cached tokens a key verified. The key's deletion calls it, so the
// deletion takes effect at once rather than within VerifiedTokenTTL.
func (v *ServiceAccountJWTVerifier) ForgetKey(keyID string) {
	v.revoke(func(_ [sha256.Size]byte, e cachedJWT) bool { return e.p.KeyID == keyID })
}

// ForgetServiceAccount drops the cached tokens of an account, for when it is disabled or
// deleted.
func (v *ServiceAccountJWTVerifier) ForgetServiceAccount(id uuid.UUID) {
	v.revoke(func(_ [sha256.Size]byte, e cachedJWT) bool { return e.p.ServiceAccountID == id })
}

// ForgetProject drops the cached tokens of every service account of a project, for when the
// project is deleted.
func (v *ServiceAccountJWTVerifier) ForgetProject(project int64) {
	v.revoke(func(_ [sha256.Size]byte, e cachedJWT) bool { return e.p.ProjectNumber == project })
}

// revoke drops the matching entries and stops verifications already running from caching
// what they found, which may predate the revocation.
func (v *ServiceAccountJWTVerifier) revoke(match func([sha256.Size]byte, cachedJWT) bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.revocations.Add(1)
	maps.DeleteFunc(v.cache, match)
}

func (v *ServiceAccountJWTVerifier) forget(match func([sha256.Size]byte, cachedJWT) bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	maps.DeleteFunc(v.cache, match)
}

func (v *ServiceAccountJWTVerifier) lookup(sum [sha256.Size]byte, now time.Time) (*Principal, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	e, ok := v.cache[sum]
	if !ok {
		return nil, false
	}
	if !now.Before(e.until) {
		delete(v.cache, sum)
		return nil, false
	}
	return clonePrincipal(&e.p), true
}

// store caches a verified token until VerifiedTokenTTL from now, and never past its exp. It
// does not when a revocation happened since gen, read before the directory was.
func (v *ServiceAccountJWTVerifier) store(sum [sha256.Size]byte, p *Principal, now time.Time, gen uint64) {
	until := now.Add(VerifiedTokenTTL)
	if p.ExpiresAt.Before(until) {
		until = p.ExpiresAt
	}
	if !until.After(now) {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.revocations.Load() != gen {
		return
	}
	if len(v.cache) >= maxCachedJWTs {
		maps.DeleteFunc(v.cache, func(_ [sha256.Size]byte, e cachedJWT) bool { return !now.Before(e.until) })
		if len(v.cache) >= maxCachedJWTs {
			return
		}
	}
	v.cache[sum] = cachedJWT{p: *clonePrincipal(p), until: until}
}

func clonePrincipal(p *Principal) *Principal {
	c := *p
	c.Scopes = slices.Clone(p.Scopes)
	return &c
}

func decodeJWTSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(seg, "="))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// plausibleTime keeps iat and exp to seconds a time.Time holds without overflow.
func plausibleTime(f float64) bool {
	return f > 0 && f < 1<<40
}

func unixTime(f float64) time.Time {
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9))
}

// clipValue bounds client-supplied values before they are logged.
func clipValue(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
