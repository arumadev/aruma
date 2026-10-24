package rules

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// lookupResult is a cached lookup. A lookup Firestore could not answer (ErrNoDocuments) is
// cached with its error, so that repeating it is free; an outage ends the evaluation and is not.
type lookupResult struct {
	doc   Value
	found bool
	err   error // an *evalError, or nil
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

// lookupFunctions are the lookups each service enables (ADR-021 §3). A lookup of another
// service is an unknown function there: a warning when the file compiles, an error when called.
var lookupFunctions = map[Service]map[string]bool{
	Firestore: {"get": true, "exists": true, "getAfter": true, "existsAfter": true},
	Storage:   {"firestore.get": true, "firestore.exists": true},
}

// isLookup reports whether name ("get", "firestore.get") is a lookup svc enables.
func isLookup(svc Service, name string) bool { return lookupFunctions[svc][name] }

// lookup calls a lookup: get and getAfter return the document, or null when it does not exist
// (the rules.firestore reference: "the document, or null if it does not exist"; the emulator
// agrees, and no deployed capture tells the two apart); exists and existsAfter whether it does.
// The argument is a path to a document of the default database. A Documents that cannot read
// Firestore (ErrNoDocuments) is an evaluation error; going past a lookup limit ends the
// evaluation with a denial (ADR-021 §4); any other failure of Documents is an outage.
func (ev *evaluator) lookup(e *callExpr, fr *frame) (Value, error) {
	if len(e.args) != 1 {
		return Value{}, errorf("Incorrect number of arguments. Received: %d. Expected: %s(path).", len(e.args), e.name)
	}
	arg, err := ev.eval(e.args[0], fr)
	if err != nil {
		return Value{}, err
	}
	segs, ok := arg.AsPath()
	if !ok {
		return Value{}, errorf(msgTypeError, arg.Kind(), KindPath)
	}
	if !isDocumentPath(segs) {
		return Value{}, errorf("Service call error. Function: [%s], Argument: [/%s].", e.name, strings.Join(segs, "/"))
	}
	doc, found, err := ev.read(segs, strings.HasSuffix(e.name, "After"))
	if err != nil {
		return Value{}, err
	}
	if strings.Contains(e.name, "exists") {
		return Bool(found), nil
	}
	if !found {
		return Null(), nil
	}
	return doc, nil
}

// isDocumentPath reports whether segs name a document of the default database:
// databases/(default)/documents and an even number of segments more. Storage reads only the
// default database (its documentation), and named databases are out of scope.
func isDocumentPath(segs []string) bool {
	return len(segs) >= 5 && len(segs)%2 == 1 &&
		segs[0] == "databases" && segs[1] == "(default)" && segs[2] == "documents"
}

// read reads a document through the request's budget: a cached lookup is free; any other counts
// against the operation's limit and the request's before Documents is asked. A lookup that
// failed with ErrNoDocuments counts once, as a document does.
func (ev *evaluator) read(segs []string, after bool) (Value, bool, error) {
	b := ev.req.Budget
	key := lookupKey{after: after, path: encodePath(segs)}
	b.mu.Lock()
	r, hit := b.cache[key]
	b.mu.Unlock()
	if hit {
		return r.doc, r.found, r.err
	}
	if err := ev.ctx.Err(); err != nil {
		return Value{}, false, &abort{msg: "evaluation cancelled", err: err}
	}
	perOperation, perRequest := lookupLimits(ev.rs.service, &ev.rs.lim)
	if ev.lookups >= perOperation {
		return Value{}, false, &abort{msg: fmt.Sprintf("evaluation exceeded the limit of %d document lookups", perOperation)}
	}
	b.mu.Lock()
	if b.used >= perRequest {
		b.mu.Unlock()
		return Value{}, false, &abort{msg: fmt.Sprintf("request exceeded the limit of %d document lookups", perRequest)}
	}
	b.used++
	b.mu.Unlock()
	ev.lookups++

	doc, found, err := ev.req.Documents.Get(ev.ctx, ev.req.Project, segs, after)
	r = lookupResult{doc: doc, found: found}
	switch {
	case errors.Is(err, ErrNoDocuments):
		r = lookupResult{err: errorf("%s", err.Error())}
	case err != nil:
		return Value{}, false, &abort{msg: "document lookup failed", err: err}
	}
	b.mu.Lock()
	if b.cache == nil {
		b.cache = map[lookupKey]lookupResult{}
	}
	b.cache[key] = r
	b.mu.Unlock()
	return r.doc, r.found, r.err
}

// encodePath encodes a document path so that no two paths share a key: each segment with its
// length before it.
func encodePath(segs []string) string {
	var sb strings.Builder
	for _, s := range segs {
		sb.WriteString(strconv.Itoa(len(s)))
		sb.WriteByte(':')
		sb.WriteString(s)
	}
	return sb.String()
}
