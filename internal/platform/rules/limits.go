package rules

import (
	"errors"
	"fmt"
)

// Firebase's limits (ADR-021 §4): their values are part of compatibility, so a configuration may
// lower them but never raise them. The ones still marked are measured by S0-A, and the values
// here change with the measurement, not the design.
const (
	firebaseCallDepth           = 20 // the limits table and rules-language; rules-conditions says 10 (S0-A)
	firebaseFunctionLets        = 10 // the Storage conditions page says any number (S0-A)
	firebaseFunctionArgs        = 7
	firebaseExpressionDepth     = 98 // the deepest nesting that compiles; 99 is refused
	firebaseMatchDepth          = 10
	firebaseMatchSegments       = 100
	firebaseMatchVariables      = 20
	firebaseSourceBytes         = 256 << 10 // 256 KiB; 256,000 or 262,144 (S0-A)
	firebaseExpressions         = 1000
	firebaseLookupsPerOperation = 10
	firebaseLookupsPerRequest   = 20
	firebaseStorageLookups      = 2
)

// Limits bound what a ruleset may hold and what one evaluation may do. Firebase's limits are
// semantic and may only be lowered; Aruma's protect the server from hostile rules, have no
// counterpart in Firebase, and may be set freely (ADR-021 §4). The configuration sets them
// under rules.limits, with the keys named in the comments.
type Limits struct {
	// Firebase's, checked when a file compiles.
	CallDepth           int // call_depth: the longest chain of function calls, on the call graph
	FunctionLets        int // function_lets: let bindings in one function
	FunctionArgs        int // function_args: parameters of one function
	ExpressionDepth     int // expression_depth: the deepest nesting of parentheses, operators, lists and ?:
	MatchDepth          int // match_depth: nested match blocks
	MatchSegments       int // match_segments: path segments of nested match blocks together
	MatchVariables      int // match_variables: path variables of nested match blocks together
	SourceBytes         int // source_bytes: the size of the rules file
	Expressions         int // expressions: expressions evaluated for one request
	LookupsPerOperation int // lookups_per_operation: Firestore documents one operation reads
	LookupsPerRequest   int // lookups_per_request: Firestore documents a batch or transaction reads
	StorageLookups      int // storage_lookups: Firestore documents one Storage evaluation reads

	// Aruma's.
	MemberChain           int   // member_chain: a chain of .field and [index] (checked when compiling)
	Work                  int64 // work: units one evaluation may spend in matches, split, replace, collections
	ValueBytes            int64 // value_bytes: one string or bytes value
	ValueElements         int   // value_elements: one list, map or set
	AllocBytes            int64 // alloc_bytes: the new values one evaluation builds
	InflightBytes         int64 // inflight_bytes: what every evaluation in the process builds together
	RegexInstructions     int   // regex_instructions: the compiled program of one pattern
	RequestSegments       int   // request_segments: the segments of a request path
	RegexCacheEntries     int   // regex_cache_entries: patterns built during evaluation that a ruleset keeps
	RegexCacheBytes       int64 // regex_cache_bytes: the estimated programs of those patterns
	CompiledRulesetsBytes int64 // compiled_rulesets_bytes: the compiled rulesets kept in memory, by weight
}

// DefaultLimits returns Firebase's limits and Aruma's defaults (ADR-021 §4).
func DefaultLimits() Limits {
	return Limits{
		CallDepth:           firebaseCallDepth,
		FunctionLets:        firebaseFunctionLets,
		FunctionArgs:        firebaseFunctionArgs,
		ExpressionDepth:     firebaseExpressionDepth,
		MatchDepth:          firebaseMatchDepth,
		MatchSegments:       firebaseMatchSegments,
		MatchVariables:      firebaseMatchVariables,
		SourceBytes:         firebaseSourceBytes,
		Expressions:         firebaseExpressions,
		LookupsPerOperation: firebaseLookupsPerOperation,
		LookupsPerRequest:   firebaseLookupsPerRequest,
		StorageLookups:      firebaseStorageLookups,

		MemberChain:           2000,
		Work:                  1 << 26,
		ValueBytes:            2 << 20,
		ValueElements:         1 << 20,
		AllocBytes:            16 << 20,
		InflightBytes:         512 << 20,
		RegexInstructions:     10_000,
		RequestSegments:       1024,
		RegexCacheEntries:     256,
		RegexCacheBytes:       4 << 20,
		CompiledRulesetsBytes: 128 << 20,
	}
}

// Validate reports every limit that is not positive, and every Firebase limit set above
// Firebase's value: raising one would allow what Firebase denies.
func (l Limits) Validate() error {
	var errs []error
	for _, f := range []struct {
		name   string
		v, max int
	}{
		{"call_depth", l.CallDepth, firebaseCallDepth},
		{"function_lets", l.FunctionLets, firebaseFunctionLets},
		{"function_args", l.FunctionArgs, firebaseFunctionArgs},
		{"expression_depth", l.ExpressionDepth, firebaseExpressionDepth},
		{"match_depth", l.MatchDepth, firebaseMatchDepth},
		{"match_segments", l.MatchSegments, firebaseMatchSegments},
		{"match_variables", l.MatchVariables, firebaseMatchVariables},
		{"source_bytes", l.SourceBytes, firebaseSourceBytes},
		{"expressions", l.Expressions, firebaseExpressions},
		{"lookups_per_operation", l.LookupsPerOperation, firebaseLookupsPerOperation},
		{"lookups_per_request", l.LookupsPerRequest, firebaseLookupsPerRequest},
		{"storage_lookups", l.StorageLookups, firebaseStorageLookups},
	} {
		switch {
		case f.v < 1:
			errs = append(errs, fmt.Errorf("%s must be at least 1", f.name))
		case f.v > f.max:
			errs = append(errs, fmt.Errorf("%s %d is above Firebase's %d, which would allow what Firebase denies", f.name, f.v, f.max))
		}
	}
	for _, f := range []struct {
		name string
		v    int64
	}{
		{"member_chain", int64(l.MemberChain)},
		{"work", l.Work},
		{"value_bytes", l.ValueBytes},
		{"value_elements", int64(l.ValueElements)},
		{"alloc_bytes", l.AllocBytes},
		{"inflight_bytes", l.InflightBytes},
		{"regex_instructions", int64(l.RegexInstructions)},
		{"request_segments", int64(l.RequestSegments)},
		{"regex_cache_entries", int64(l.RegexCacheEntries)},
		{"regex_cache_bytes", l.RegexCacheBytes},
		{"compiled_rulesets_bytes", l.CompiledRulesetsBytes},
	} {
		if f.v < 1 {
			errs = append(errs, fmt.Errorf("%s must be at least 1", f.name))
		}
	}
	return errors.Join(errs...)
}
