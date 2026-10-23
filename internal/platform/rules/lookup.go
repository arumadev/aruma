package rules

import (
	"context"
	"errors"
	"sync"
)

// Documents reads Firestore documents for rules. project is the project the request is for;
// path is the whole document path, /databases/(default)/documents/..., as decoded segments;
// after selects the state after the request's pending writes (getAfter, existsAfter).
//
// A document that does not exist is found == false with a nil error. Any error other than
// ErrNoDocuments is an outage: Evaluate returns it and the service answers UNAVAILABLE.
type Documents interface {
	Get(ctx context.Context, project int64, path []string, after bool) (doc Value, found bool, err error)
}

// ErrNoDocuments is what a Documents answers when Firestore cannot be read: an evaluation error
// in the condition, never an outage.
var ErrNoDocuments = errors.New("rules: Firestore documents are not available")

// NoDocuments is the Documents Storage gets until v0.4: every lookup fails with ErrNoDocuments,
// which denies unless || absorbs it. A nil Documents is an invalid request, not a synonym.
type NoDocuments struct{}

// Get fails with ErrNoDocuments.
func (NoDocuments) Get(context.Context, int64, []string, bool) (Value, bool, error) {
	return Value{}, false, ErrNoDocuments
}

// Budget holds Firebase's limits on document lookups for one request, and the lookups made so
// far: a service makes one per request and passes it to each operation of the request, so a
// batch or a transaction shares it (ADR-021 §3). Only Firebase's limits are shared; Aruma's are
// each evaluation's own (meter).
//
// A lookup answered from the cache does not count ("cached calls do not count"). get() and
// exists() on the same path in the same state are one lookup. The limits are not the budget's:
// Evaluate takes them from the ruleset (lookupLimits), so a budget made for one service cannot
// give another service's limits. Each evaluation counts its own lookups against the operation's
// limit; the budget counts the request's against the request's.
type Budget struct {
	mu    sync.Mutex
	used  int // lookups the request has made, cached ones excluded
	cache map[lookupKey]lookupResult
}

// lookupKey names one cached lookup.
type lookupKey struct {
	after bool
	path  string // the document path, encoded so that no two paths share it
}

// lookupResult is a cached lookup. Failed lookups are not cached.
type lookupResult struct {
	doc   Value
	found bool
}

// NewBudget returns an empty budget for one request.
func NewBudget() *Budget {
	return &Budget{cache: map[lookupKey]lookupResult{}}
}

// lookupLimits returns the lookups one operation and one request of svc may make: in Firestore
// 10 and 20; in Storage 2 for both, its requests being single operations ("No more than two
// Firestore documents may be accessed in a single Rules evaluation"); none in any other.
func lookupLimits(svc Service, lim *Limits) (perOperation, perRequest int) {
	switch svc {
	case Firestore:
		return lim.LookupsPerOperation, lim.LookupsPerRequest
	case Storage:
		return lim.StorageLookups, lim.StorageLookups
	}
	return 0, 0
}
