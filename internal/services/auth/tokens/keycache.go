package tokens

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"time"

	"github.com/osama300/aurma-project/internal/services/auth/queries"
)

// The published keys Verify checks against, cached per project and purpose.
//
// Consistency: every key change goes through this Manager (bootstrap, Rotate, Maintain), and
// each clears the project's entry once it has committed, so an emergency rotation stops its
// tokens at once. A load that overlaps a change is not kept: keysChanged bumps gen, and a load
// is stored only if gen is what it was when the load began. keyCacheTTL bounds what this
// process cannot see, a key changed by hand in the database or by another process, to the
// minute ADR-004 already allows external verifiers through the JWKS max-age.

// keyCacheTTL is how long a project's published keys are trusted without a reload.
const keyCacheTTL = time.Minute

// keySet is a project's published keys of one purpose: next, active and retired.
type keySet struct {
	keys     map[string]*rsa.PublicKey
	loadedAt time.Time
}

// publishedKeys returns the project's published keys of the purpose, from the cache when fresh.
func (m *Manager) publishedKeys(ctx context.Context, project int64, purpose Purpose) (*keySet, error) {
	k := signerKey{project, purpose}
	m.mu.Lock()
	set, gen := m.published[k], m.gen
	m.mu.Unlock()
	if set != nil && m.now().Sub(set.loadedAt) < keyCacheTTL {
		return set, nil
	}
	rows, err := m.q.ListSigningKeys(ctx, queries.ListSigningKeysParams{ProjectNumber: project, Purpose: string(purpose)})
	if err != nil {
		return nil, err
	}
	set = &keySet{keys: make(map[string]*rsa.PublicKey, len(rows)), loadedAt: m.now()}
	for _, r := range rows {
		var j JWK
		if err := json.Unmarshal(r.PublicJwk, &j); err != nil {
			return nil, err
		}
		pub, err := j.publicKey()
		if err != nil {
			return nil, err
		}
		set.keys[r.Kid] = pub
	}
	m.mu.Lock()
	if m.gen == gen {
		if m.published == nil {
			m.published = map[signerKey]*keySet{}
		}
		m.published[k] = set
	}
	m.mu.Unlock()
	return set, nil
}

// keysChanged drops the project's cached keys of the purpose after a change has committed.
func (m *Manager) keysChanged(project int64, purpose Purpose) {
	m.mu.Lock()
	delete(m.published, signerKey{project, purpose})
	m.gen++
	m.mu.Unlock()
}
