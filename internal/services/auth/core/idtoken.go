package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/idtoken"
	"github.com/arumadev/aruma/internal/services/auth/queries"
	"github.com/arumadev/aruma/internal/services/auth/tokens"
)

// Auth's side of the ID token contract (platform/idtoken), for the services that accept its
// tokens. Auth does not know who they are: internal/app hands them IDTokenVerifier and
// Revocations.
//
// Verification normally touches no database. The signing keys are cached by tokens.Manager;
// the project id and each user's revocation state (exists, disabled, tokensValidAfter,
// created) are cached here.
//
// Consistency: every change to a user's revocation state goes through this Service, which
// calls userChanged once the change has committed. That drops the cached state, so the next
// verification reads it afresh, and publishes the revocation. A load that overlaps a change
// is not kept (stateCache.gen), so it cannot pin the state from before it. stateCacheTTL bounds
// what this process cannot see: a change made by hand in the database, or a project purged.
// One node sees every change while Aruma runs one (ADR-019); several will need the changes to
// travel between them before this cache can be trusted.

// stateCacheTTL is how long a project id or a user's revocation state is trusted without a
// reload: the minute ADR-004 already gives external verifiers through the JWKS max-age.
const stateCacheTTL = time.Minute

// stateCacheMax bounds the cached users; past it, arbitrary entries make room.
const stateCacheMax = 100_000

// idTokenSkew is how far iat and auth_time may lie in the future, in seconds, as in VerifyIDToken.
const idTokenSkew = 300

// IDTokenVerifier verifies the project's ID tokens for another service, as Firebase's
// verifyIdToken(token, checkRevoked=true) does.
func (s *Service) IDTokenVerifier() idtoken.Verifier { return idTokenVerifier{s} }

// Revocations delivers, per project, the users whose tokens stopped being valid.
func (s *Service) Revocations() idtoken.Revocations { return s.revocations }

type idTokenVerifier struct{ s *Service }

// idTokenClaims are the claims the verifier checks; the rest pass through in Claims.Token.
type idTokenClaims struct {
	Iss           string `json:"iss"`
	Aud           string `json:"aud"`
	Sub           string `json:"sub"`
	Iat           int64  `json:"iat"`
	Exp           int64  `json:"exp"`
	AuthTime      int64  `json:"auth_time"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Firebase      struct {
		SignInProvider string `json:"sign_in_provider"`
		Tenant         string `json:"tenant"`
	} `json:"firebase"`
}

// Verify implements idtoken.Verifier. The checks run in Firebase's order: signature, issuer
// and audience, times, then the user: deleted, disabled, revoked.
func (v idTokenVerifier) Verify(ctx context.Context, project int64, token string) (*idtoken.Claims, error) {
	s := v.s
	id, err := s.ids.projectID(ctx, s, project)
	if apierr.CodeOf(err) == apierr.NotFound {
		return nil, idtoken.ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	payload, err := s.tokens.Verify(ctx, project, tokens.IDToken, token)
	if errors.Is(err, tokens.ErrMalformed) || errors.Is(err, tokens.ErrSignature) {
		return nil, idtoken.ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	var c idTokenClaims
	var all map[string]any
	if json.Unmarshal(payload, &c) != nil || decodeNumbers(payload, &all) != nil {
		return nil, idtoken.ErrInvalid
	}
	if c.Iss != s.Issuer(Project{Number: project, ID: id}) || c.Aud != id || !validUID(c.Sub) {
		return nil, idtoken.ErrInvalid
	}
	now := s.now().Unix()
	if c.Exp <= now {
		return nil, idtoken.ErrExpired
	}
	if c.Iat > now+idTokenSkew || c.AuthTime <= 0 || c.AuthTime > now+idTokenSkew {
		return nil, idtoken.ErrInvalid
	}
	st, err := s.states.get(ctx, s, project, c.Firebase.Tenant, c.Sub)
	switch {
	case err != nil:
		return nil, err
	case !st.exists:
		return nil, idtoken.ErrUserNotFound
	case st.disabled:
		return nil, idtoken.ErrDisabled
	case c.AuthTime < st.validAfter || st.createdAt > c.Iat:
		// auth_time, as Firebase's checkRevoked compares: the session a revocation itself
		// returns starts at the revocation point and survives it. A token older than its
		// user belongs to an earlier user of the same uid.
		return nil, idtoken.ErrRevoked
	}
	return &idtoken.Claims{
		UID: c.Sub, Tenant: c.Firebase.Tenant, SignInProvider: c.Firebase.SignInProvider,
		Email: c.Email, EmailVerified: c.EmailVerified,
		IssuedAt: time.Unix(c.Iat, 0), AuthTime: time.Unix(c.AuthTime, 0), Expires: time.Unix(c.Exp, 0),
		Token: all,
	}, nil
}

// decodeNumbers decodes JSON keeping each number as its text (json.Number): a whole claim
// beyond 2^53 stays exact, as it does in Firebase's security rules (S7-R: 9007199254740993 is
// that int there).
func decodeNumbers(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return errors.New("trailing data")
	}
	return nil
}

// userChanged runs after a change to a user's revocation state has committed: it drops the
// cached state and, when the change ends sessions (cause is set), publishes it. validAfter is
// the user's new tokensValidAfter for the causes that keep the user.
func (s *Service) userChanged(p Project, uid string, cause idtoken.Cause, validAfter time.Time) {
	s.states.forget(p.Number, p.Tenant, uid)
	if cause == "" {
		return
	}
	r := idtoken.Revocation{Project: p.Number, Tenant: p.Tenant, UID: uid, Cause: cause}
	if cause != idtoken.Disabled && cause != idtoken.Deleted {
		r.ValidAfter = validAfter
	}
	s.revocations.Publish(r)
}

// revocationCause names the strongest reason an admin update ends sessions, or "" when it
// ends none.
func revocationCause(disabled, revoked, password, email bool) idtoken.Cause {
	switch {
	case disabled:
		return idtoken.Disabled
	case revoked:
		return idtoken.TokensRevoked
	case password:
		return idtoken.PasswordChanged
	case email:
		return idtoken.EmailChanged
	}
	return ""
}

// projectIDs caches project ids by number. An id never changes; a deleted project's tokens
// stop verifying within stateCacheTTL, and its databases are refused by their own service.
type projectIDs struct {
	mu sync.Mutex
	m  map[int64]projectIDEntry
}

type projectIDEntry struct {
	id       string
	loadedAt time.Time
}

func (c *projectIDs) projectID(ctx context.Context, s *Service, number int64) (string, error) {
	c.mu.Lock()
	e, ok := c.m[number]
	c.mu.Unlock()
	if ok && s.now().Sub(e.loadedAt) < stateCacheTTL {
		return e.id, nil
	}
	id, err := s.control.ProjectID(ctx, number)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	if c.m == nil || len(c.m) >= stateCacheMax {
		c.m = map[int64]projectIDEntry{}
	}
	c.m[number] = projectIDEntry{id: id, loadedAt: s.now()}
	c.mu.Unlock()
	return id, nil
}

// userState is what decides whether a user's tokens still verify.
type userState struct {
	exists     bool
	disabled   bool
	validAfter int64 // tokensValidAfter, Unix seconds
	createdAt  int64 // Unix seconds
	loadedAt   time.Time
}

type userKey struct {
	project     int64
	tenant, uid string
}

// stateCache caches users' revocation state. gen counts the changes, so a load that overlaps
// one is not kept. A missing user is not cached: only a deleted user's unexpired token asks
// for one, and a uid may come back.
type stateCache struct {
	mu  sync.Mutex
	m   map[userKey]userState
	gen uint64
}

func (c *stateCache) get(ctx context.Context, s *Service, project int64, tenant, uid string) (userState, error) {
	k := userKey{project, tenant, uid}
	c.mu.Lock()
	st, ok := c.m[k]
	gen := c.gen
	c.mu.Unlock()
	if ok && s.now().Sub(st.loadedAt) < stateCacheTTL {
		return st, nil
	}
	raw, err := s.q.GetUser(ctx, queries.GetUserParams{ProjectNumber: project, TenantID: tenant, Uid: uid})
	if errors.Is(err, pgx.ErrNoRows) {
		return userState{}, nil
	}
	if err != nil {
		return userState{}, err
	}
	st = userState{exists: true, disabled: raw.Disabled, validAfter: raw.TokensValidAfter.Unix(),
		createdAt: raw.CreatedAt.Unix(), loadedAt: s.now()}
	c.mu.Lock()
	if c.gen == gen {
		if c.m == nil {
			c.m = map[userKey]userState{}
		}
		if len(c.m) >= stateCacheMax {
			// Map iteration order is unspecified: an arbitrary tenth goes.
			n := stateCacheMax / 10
			for old := range c.m {
				if n == 0 {
					break
				}
				delete(c.m, old)
				n--
			}
		}
		c.m[k] = st
	}
	c.mu.Unlock()
	return st, nil
}

func (c *stateCache) forget(project int64, tenant, uid string) {
	c.mu.Lock()
	delete(c.m, userKey{project, tenant, uid})
	c.gen++
	c.mu.Unlock()
}
