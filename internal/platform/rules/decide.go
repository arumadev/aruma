package rules

import (
	"context"
	"errors"
	"fmt"
)

// errInvalidRequest is what Evaluate returns for a request it cannot decide: an outage of the
// service that built it, never a denial.
var errInvalidRequest = errors.New("rules: invalid request")

// Evaluate decides one request: the match blocks whose path matches, in file order; the allow
// statements that cover the method; each condition once, the first true one granting.
//
// The error is an outage only, never a denial: a cancelled context, a Documents failure other
// than ErrNoDocuments, ErrOverloaded, or an invalid request (no Documents, Budget or Inflight,
// or a path longer than the limit). The service answers UNAVAILABLE and never allows.
func (rs *Ruleset) Evaluate(ctx context.Context, req *Request) (Decision, error) {
	switch {
	case rs == nil:
		return Decision{}, fmt.Errorf("%w: no ruleset", errInvalidRequest)
	case req == nil:
		return Decision{}, fmt.Errorf("%w: no request", errInvalidRequest)
	case req.Documents == nil || req.Budget == nil || req.Inflight == nil:
		return Decision{}, fmt.Errorf("%w: Documents, Budget and Inflight are required", errInvalidRequest)
	case req.Method < MethodGet || req.Method > MethodDelete:
		return Decision{}, fmt.Errorf("%w: method %d", errInvalidRequest, req.Method)
	case len(req.Path) > rs.lim.RequestSegments:
		return Decision{}, fmt.Errorf("%w: a path of %d segments, above the limit of %d", errInvalidRequest, len(req.Path), rs.lim.RequestSegments)
	}
	if err := ctx.Err(); err != nil {
		return Decision{}, err
	}
	m := newMeter(&rs.lim, req.Inflight, rs.regexes)
	defer m.release()
	ev := &evaluator{ctx: ctx, rs: rs, req: req, m: m}
	trace := req.Trace != TraceNone

	var d Decision
	for _, b := range rs.blocks {
		if !b.methods.has(req.Method) {
			continue
		}
		vars, ok := b.match(req.Path, rs.version)
		if !ok {
			continue
		}
		ev.vars = vars
		for _, a := range b.allows {
			if !a.methods.has(req.Method) {
				continue
			}
			v, err := ev.condition(a.a.cond)
			res := StatementResult{Outcome: OutcomeError}
			var ab *abort
			switch {
			case errors.As(err, &ab):
				if ab.err != nil {
					return Decision{}, err
				}
				// A limit: the whole evaluation is denied (ADR-021 §4).
				res.Message = ab.msg
			case err != nil:
				res.Message = err.Error()
			default:
				if b, ok := v.AsBool(); !ok {
					res.Message = notBool(v).Error()
				} else if b {
					res.Outcome = OutcomeTrue
				} else {
					res.Outcome = OutcomeFalse
				}
			}
			if trace {
				res.Position = position(rs.src, a.a.span)
				res.Methods = a.names
				d.Statements = append(d.Statements, res)
			}
			if ab != nil {
				return Decision{Statements: d.Statements}, nil
			}
			if res.Outcome == OutcomeTrue {
				d.Allow = true
				return d, nil
			}
		}
	}
	// Nothing granted: msgNoMatch when no statement covered the request.
	return d, nil
}
