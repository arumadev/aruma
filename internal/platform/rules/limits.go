package rules

import (
	"errors"
	"fmt"
	"math"
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
	firebaseRulesetsPerProject  = 2500 // manage-deploy: "up to 2,500 rulesets"
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
	RulesetsPerProject  int // rulesets_per_project: the rulesets one project keeps (tenancy)

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
	RegexBytes            int64 // regex_bytes: the estimated programs of one ruleset's literal patterns together
	CompiledRulesetsBytes int64 // compiled_rulesets_bytes: the compiled rulesets kept in memory, by weight
}

// rulesetShare is the part of compiled_rulesets_bytes one ruleset may weigh (Weight): a quarter,
// so that the cache keeps the four heaviest rulesets that compile at once, and no ruleset that
// compiles is too heavy to keep and compiled again on every request (the review's SR3-02).
const rulesetShare = 4

// rulesetBytes is the most a ruleset may weigh: Compile refuses a heavier one.
func (l *Limits) rulesetBytes() int64 { return l.CompiledRulesetsBytes / rulesetShare }

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
		RulesetsPerProject:  firebaseRulesetsPerProject,

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
		RegexBytes:            4 << 20,
		CompiledRulesetsBytes: 128 << 20,
	}
}

// Validate reports every limit that is not positive, every Firebase limit set above Firebase's
// value (raising one would allow what Firebase denies), an Inflight cap below one evaluation's
// allocation, a value cap above it, and a cache of compiled rulesets too small for a ruleset
// whose literal patterns use their budget.
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
		{"rulesets_per_project", l.RulesetsPerProject, firebaseRulesetsPerProject},
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
		{"regex_bytes", l.RegexBytes},
		{"compiled_rulesets_bytes", l.CompiledRulesetsBytes},
	} {
		if f.v < 1 {
			errs = append(errs, fmt.Errorf("%s must be at least 1", f.name))
		}
	}
	// An evaluation that may build more than the process allows would fail as overloaded every
	// time, an UNAVAILABLE the SDKs retry forever; one value larger than an evaluation may build
	// could never be built.
	if l.InflightBytes < l.AllocBytes {
		errs = append(errs, fmt.Errorf("inflight_bytes %d must be at least alloc_bytes %d", l.InflightBytes, l.AllocBytes))
	}
	if l.ValueBytes > l.AllocBytes {
		errs = append(errs, fmt.Errorf("value_bytes %d must not exceed alloc_bytes %d", l.ValueBytes, l.AllocBytes))
	}
	// A ruleset weighs at least its source, the cap of its cache of patterns and its literal
	// patterns with their forms for lone surrogates (Weight): past a quarter of the cache, one
	// whose patterns use their budget would never compile.
	if need := l.leastWeight(); l.CompiledRulesetsBytes > 0 && need > l.rulesetBytes() {
		errs = append(errs, fmt.Errorf("compiled_rulesets_bytes %d must be at least %d times source_bytes, regex_cache_bytes and %d times regex_bytes together (%d)",
			l.CompiledRulesetsBytes, rulesetShare, 1+loneCacheBlocks, need))
	}
	return errors.Join(errs...)
}

// leastWeight is what a ruleset whose source and literal patterns use their limits weighs at
// least, saturating rather than overflowing.
func (l *Limits) leastWeight() int64 {
	w := int64(0)
	for _, n := range []int64{int64(l.SourceBytes), l.RegexCacheBytes, literalWeight(l.RegexBytes)} {
		if n > math.MaxInt64-w {
			return math.MaxInt64
		}
		w += max(n, 0)
	}
	return w
}
