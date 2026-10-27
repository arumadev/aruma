package rules

import (
	"context"
	"errors"
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
// multi-document read, a batch or a transaction shares it (ADR-021 §3). Only Firebase's limits
// are shared; Aruma's are each evaluation's own (meter).
//
// The limits count distinct documents (production, f5-rules-lookup-limits.json): get() and
// exists() of one path, and getAfter() and existsAfter() of it, are one document, read from
// Documents once per state. An operation counts every document it looks up against its own
// limit, whether another operation of the request read it first or not (commit.r1a-r11a: the
// second write alone looks up 11); the request counts each document once against its limit. A
// document the request writes (Exempt) counts against neither (commit.peer-a-peer-b,
// write.self-after). The limits are not the budget's: Evaluate takes them from the ruleset
// (lookupLimits), so a budget made for one service cannot give another service's limits; a
// pooled budget only lifts the operation's limit to the request's.
type Budget struct {
	mu      sync.Mutex
	pooled  bool
	counted map[string]bool // the documents the request has counted, by encoded path
	exempt  map[string]bool // the documents the request writes, which count against no limit
	cache   map[lookupKey]lookupResult
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

// NewBudget returns an empty budget for one request whose operations each have the operation's
// limit as well as the request's: a get of one document, a commit's writes.
func NewBudget() *Budget {
	return &Budget{counted: map[string]bool{}, exempt: map[string]bool{}, cache: map[lookupKey]lookupResult{}}
}

// NewPooledBudget returns an empty budget for one request whose operations have the request's
// limit alone: a read of several documents (batch-get-r1a-r20a: 10 and 20 lookups, 20
// distinct, allowed), a list query (run-query-20).
func NewPooledBudget() *Budget {
	b := NewBudget()
	b.pooled = true
	return b
}

// Exempt makes the lookups of the document at path (databases/(default)/documents/...) free:
// one the request writes.
func (b *Budget) Exempt(path []string) {
	b.mu.Lock()
	b.exempt[encodePath(path)] = true
	b.mu.Unlock()
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

// lookup calls a lookup: get and getAfter return the document, and are an evaluation error when
// it does not exist (production: get(missing).data.v == 1 || true allows, the same alone
// denies; not the reference's "null"); exists and existsAfter whether it does. The argument is
// a path to a document of the default database. A Documents that cannot read Firestore
// (ErrNoDocuments) and going past a lookup limit are evaluation errors, which || absorbs
// (ADR-021 §4); any other failure of Documents is an outage.
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
		return Value{}, errorf("Service call error. Function: [%s], Argument: [/%s].", e.name, clip(strings.Join(segs, "/")))
	}
	doc, found, err := ev.read(segs, strings.HasSuffix(e.name, "After"))
	if err != nil {
		return Value{}, err
	}
	if strings.Contains(e.name, "exists") {
		return Bool(found), nil
	}
	if !found {
		return Value{}, errorf("Service call error. Function: [%s], Argument: [/%s]: the document does not exist.", e.name, clip(strings.Join(segs, "/")))
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

// read reads a document through the request's budget. A document the request writes is free;
// any other counts once against the operation's limit and once against the request's, before
// it is first looked up by each, and is read from Documents once per state. Going past a limit
// is an evaluation error, as production's is (lookup-limits miss-exists, live-after: absorbed by
// ||), and is not cached: nothing was read. A lookup that failed with ErrNoDocuments counts
// once, as a document does.
func (ev *evaluator) read(segs []string, after bool) (Value, bool, error) {
	if err := ev.ctx.Err(); err != nil {
		return Value{}, false, &abort{msg: "evaluation cancelled", err: err}
	}
	b := ev.req.Budget
	path := encodePath(segs)
	key := lookupKey{after: after, path: path}
	perOperation, perRequest := lookupLimits(ev.rs.service, &ev.rs.lim)
	b.mu.Lock()
	if b.pooled {
		perOperation = perRequest
	}
	if !b.exempt[path] && !ev.lookups[path] {
		if len(ev.lookups) >= perOperation {
			b.mu.Unlock()
			return Value{}, false, errorf("evaluation exceeded the limit of %d document lookups", perOperation)
		}
		if !b.counted[path] {
			if len(b.counted) >= perRequest {
				b.mu.Unlock()
				return Value{}, false, errorf("request exceeded the limit of %d document lookups", perRequest)
			}
			b.counted[path] = true
		}
		if ev.lookups == nil {
			ev.lookups = map[string]bool{}
		}
		ev.lookups[path] = true
	}
	r, hit := b.cache[key]
	b.mu.Unlock()
	if hit {
		return r.doc, r.found, r.err
	}

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
