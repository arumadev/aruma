// Package tokens issues and verifies Aruma's user tokens (ADR-004): per-project RSA keys for
// each purpose, a next/active/retired/destroyed key cycle, RS256 JWTs and the published JWKS.
package tokens

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osama300/aurma-project/internal/platform/crypto"
	"github.com/osama300/aurma-project/internal/services/auth/queries"
	"github.com/osama300/aurma-project/internal/store"
)

// Purpose separates the keys of ID tokens from those of session cookies, so one can never pass
// for the other.
type Purpose string

const (
	IDToken       Purpose = "id_token"
	SessionCookie Purpose = "session_cookie"
)

// Lifetime is how long a token of the purpose may live; retired keys stay published this long.
func (p Purpose) Lifetime() time.Duration {
	if p == SessionCookie {
		return 14 * 24 * time.Hour
	}
	return time.Hour
}

// RotateEvery is the age at which an active key is replaced (ADR-004, proposed value).
const RotateEvery = 30 * 24 * time.Hour

// Manager signs and verifies tokens with the project's keys.
type Manager struct {
	pool *pgxpool.Pool
	q    *queries.Queries
	keys *crypto.Keys
	now  func() time.Time

	mu      sync.Mutex
	signers map[signerKey]*signer
}

type signerKey struct {
	project int64
	purpose Purpose
}

type signer struct {
	kid string
	key *rsa.PrivateKey
}

// NewManager returns a token manager.
func NewManager(pool *pgxpool.Pool, keys *crypto.Keys) *Manager {
	return &Manager{pool: pool, q: queries.New(pool), keys: keys, now: time.Now, signers: map[signerKey]*signer{}}
}

func aad(kid string) []byte { return []byte("auth.signing_keys|private_key|" + kid) }

// Sign issues a token for the project with its active key, creating the key on first use.
func (m *Manager) Sign(ctx context.Context, project int64, purpose Purpose, claims any) (string, error) {
	s, err := m.activeSigner(ctx, project, purpose)
	if err != nil {
		return "", err
	}
	return sign(s.kid, s.key, claims)
}

func (m *Manager) activeSigner(ctx context.Context, project int64, purpose Purpose) (*signer, error) {
	k := signerKey{project, purpose}
	m.mu.Lock()
	s, ok := m.signers[k]
	m.mu.Unlock()
	if ok {
		return s, nil
	}
	rows, err := m.q.ListSigningKeys(ctx, queries.ListSigningKeysParams{ProjectNumber: project, Purpose: string(purpose)})
	if err != nil {
		return nil, err
	}
	var active *queries.AuthSigningKey
	for i := range rows {
		if rows[i].State == "active" {
			active = &rows[i]
		}
	}
	if active == nil {
		created, err := m.bootstrap(ctx, project, purpose)
		if err != nil {
			return nil, err
		}
		active = created
	}
	priv, err := m.decrypt(*active)
	if err != nil {
		return nil, err
	}
	s = &signer{kid: active.Kid, key: priv}
	m.mu.Lock()
	m.signers[k] = s
	m.mu.Unlock()
	return s, nil
}

// bootstrap creates the first active key and its successor. Two callers racing here are
// settled by the one-active-key unique index: the loser reads the winner's key.
func (m *Manager) bootstrap(ctx context.Context, project int64, purpose Purpose) (*queries.AuthSigningKey, error) {
	var active queries.AuthSigningKey
	err := store.InTx(ctx, m.pool, func(tx pgx.Tx) error {
		q := m.q.WithTx(tx)
		var err error
		if active, err = m.newKey(ctx, q, project, purpose, "active"); err != nil {
			return err
		}
		_, err = m.newKey(ctx, q, project, purpose, "next")
		return err
	})
	if _, ok := store.UniqueViolation(err); ok {
		rows, err := m.q.ListSigningKeys(ctx, queries.ListSigningKeysParams{ProjectNumber: project, Purpose: string(purpose)})
		if err != nil {
			return nil, err
		}
		for i := range rows {
			if rows[i].State == "active" {
				return &rows[i], nil
			}
		}
		return nil, errors.New("tokens: no active key after concurrent creation")
	}
	if err != nil {
		return nil, err
	}
	return &active, nil
}

func (m *Manager) newKey(ctx context.Context, q *queries.Queries, project int64, purpose Purpose, state string) (queries.AuthSigningKey, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return queries.AuthSigningKey{}, err
	}
	kid := hex.EncodeToString(crypto.RandomBytes(20))
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return queries.AuthSigningKey{}, err
	}
	enc, err := m.keys.Seal(der, aad(kid))
	if err != nil {
		return queries.AuthSigningKey{}, err
	}
	jwk, err := json.Marshal(toJWK(kid, &priv.PublicKey))
	if err != nil {
		return queries.AuthSigningKey{}, err
	}
	var activated *time.Time
	if state == "active" {
		now := m.now()
		activated = &now
	}
	return q.InsertSigningKey(ctx, queries.InsertSigningKeyParams{
		Kid: kid, ProjectNumber: project, Purpose: string(purpose), State: state,
		PrivateKeyEnc: enc, PublicJwk: jwk, ActivatedAt: activated,
	})
}

func (m *Manager) decrypt(k queries.AuthSigningKey) (*rsa.PrivateKey, error) {
	if k.PrivateKeyEnc == nil {
		return nil, fmt.Errorf("tokens: key %s has no private part", k.Kid)
	}
	der, err := m.keys.Open(k.PrivateKeyEnc, aad(k.Kid))
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("tokens: not an RSA key")
	}
	return rsaKey, nil
}

// JWKS returns the published keys of a project: next, active and retired. Destroyed keys are
// gone, so tokens they signed no longer verify anywhere.
func (m *Manager) JWKS(ctx context.Context, project int64, purpose Purpose) ([]JWK, error) {
	if _, err := m.activeSigner(ctx, project, purpose); err != nil {
		return nil, err
	}
	rows, err := m.q.ListSigningKeys(ctx, queries.ListSigningKeysParams{ProjectNumber: project, Purpose: string(purpose)})
	if err != nil {
		return nil, err
	}
	out := make([]JWK, 0, len(rows))
	for _, r := range rows {
		var j JWK
		if err := json.Unmarshal(r.PublicJwk, &j); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// Verify checks a token's signature against the project's published keys of the purpose and
// returns its payload. Time and audience checks are the caller's (Claims.Check).
func (m *Manager) Verify(ctx context.Context, project int64, purpose Purpose, token string) ([]byte, error) {
	p, err := parse(token)
	if err != nil {
		return nil, err
	}
	row, err := m.q.GetSigningKey(ctx, queries.GetSigningKeyParams{ProjectNumber: project, Kid: p.header.Kid})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSignature
		}
		return nil, err
	}
	if row.Purpose != string(purpose) || row.State == "destroyed" {
		return nil, ErrSignature
	}
	var j JWK
	if err := json.Unmarshal(row.PublicJwk, &j); err != nil {
		return nil, err
	}
	pub, err := j.publicKey()
	if err != nil {
		return nil, err
	}
	if !p.verify(pub) {
		return nil, ErrSignature
	}
	return p.payload, nil
}

// Rotate advances the key cycle. Normally next becomes active and active becomes retired, so
// tokens already issued keep verifying until they expire. In an emergency the active key is
// destroyed at once and every token it signed stops verifying.
func (m *Manager) Rotate(ctx context.Context, project int64, purpose Purpose, emergency bool) error {
	err := store.InTx(ctx, m.pool, func(tx pgx.Tx) error {
		q := m.q.WithTx(tx)
		rows, err := q.ListSigningKeys(ctx, queries.ListSigningKeysParams{ProjectNumber: project, Purpose: string(purpose)})
		if err != nil {
			return err
		}
		var active, next *queries.AuthSigningKey
		for i := range rows {
			switch rows[i].State {
			case "active":
				active = &rows[i]
			case "next":
				next = &rows[i]
			}
		}
		if active != nil {
			state := "retired"
			if emergency {
				state = "destroyed"
			}
			if err := q.SetSigningKeyState(ctx, queries.SetSigningKeyStateParams{ProjectNumber: project, Kid: active.Kid, State: state}); err != nil {
				return err
			}
		}
		if next != nil {
			if err := q.SetSigningKeyState(ctx, queries.SetSigningKeyStateParams{ProjectNumber: project, Kid: next.Kid, State: "active"}); err != nil {
				return err
			}
		} else if _, err := m.newKey(ctx, q, project, purpose, "active"); err != nil {
			return err
		}
		_, err = m.newKey(ctx, q, project, purpose, "next")
		return err
	})
	m.mu.Lock()
	delete(m.signers, signerKey{project, purpose})
	m.mu.Unlock()
	return err
}

// Maintain rotates keys older than RotateEvery and destroys retired keys whose tokens have all
// expired. The server runs it periodically.
func (m *Manager) Maintain(ctx context.Context) error {
	cutoff := m.now().Add(-RotateEvery)
	due, err := m.q.ListRotationDue(ctx, &cutoff)
	if err != nil {
		return err
	}
	for _, d := range due {
		if err := m.Rotate(ctx, d.ProjectNumber, Purpose(d.Purpose), false); err != nil {
			return err
		}
	}
	for _, p := range []Purpose{IDToken, SessionCookie} {
		before := m.now().Add(-p.Lifetime())
		old, err := m.q.ListRetiredToDestroy(ctx, queries.ListRetiredToDestroyParams{Before: &before, Purpose: string(p)})
		if err != nil {
			return err
		}
		for _, o := range old {
			if err := m.q.SetSigningKeyState(ctx, queries.SetSigningKeyStateParams{ProjectNumber: o.ProjectNumber, Kid: o.Kid, State: "destroyed"}); err != nil {
				return err
			}
		}
	}
	return nil
}
