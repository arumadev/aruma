package rules

import "context"

// Evaluate decides one request: the match blocks whose path matches, in file order; the allow
// statements that cover the method; each condition once, the first true one granting.
//
// The error is an outage only, never a denial: a cancelled context, a Documents failure other
// than ErrNoDocuments, ErrOverloaded, or an invalid request (no Documents, Budget or Inflight,
// or a path longer than the limit). The service answers UNAVAILABLE and never allows.
// S1-C replaces this stub.
func (rs *Ruleset) Evaluate(ctx context.Context, req *Request) (Decision, error) {
	panic("S1-C: Evaluate")
}
