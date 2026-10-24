package rules

import (
	"strings"
	"testing"
)

func TestDefaultLimitsFollowADR021(t *testing.T) {
	l := DefaultLimits()
	if err := l.Validate(); err != nil {
		t.Fatalf("the defaults do not validate: %v", err)
	}
	want := Limits{
		CallDepth: 20, FunctionLets: 10, FunctionArgs: 7, ExpressionDepth: 98, MatchDepth: 10,
		MatchSegments: 100, MatchVariables: 20, SourceBytes: 262_144, Expressions: 1000,
		LookupsPerOperation: 10, LookupsPerRequest: 20, StorageLookups: 2, RulesetsPerProject: 2500,
		MemberChain: 2000, Work: 1 << 26, ValueBytes: 2 << 20, ValueElements: 1 << 20,
		AllocBytes: 16 << 20, InflightBytes: 512 << 20, RegexInstructions: 10_000,
		RequestSegments: 1024, RegexCacheEntries: 256, RegexCacheBytes: 4 << 20, RegexBytes: 4 << 20,
		CompiledRulesetsBytes: 128 << 20,
	}
	if l != want {
		t.Errorf("DefaultLimits() = %+v\nwant %+v", l, want)
	}
}

func TestValidateRefusesRaisedFirebaseLimits(t *testing.T) {
	for _, c := range []struct {
		name string
		set  func(*Limits)
	}{
		{"call_depth", func(l *Limits) { l.CallDepth = 21 }},
		{"function_lets", func(l *Limits) { l.FunctionLets = 11 }},
		{"function_args", func(l *Limits) { l.FunctionArgs = 8 }},
		{"expression_depth", func(l *Limits) { l.ExpressionDepth = 99 }},
		{"match_depth", func(l *Limits) { l.MatchDepth = 11 }},
		{"match_segments", func(l *Limits) { l.MatchSegments = 101 }},
		{"match_variables", func(l *Limits) { l.MatchVariables = 21 }},
		{"source_bytes", func(l *Limits) { l.SourceBytes = 262_145 }},
		{"expressions", func(l *Limits) { l.Expressions = 1001 }},
		{"lookups_per_operation", func(l *Limits) { l.LookupsPerOperation = 11 }},
		{"lookups_per_request", func(l *Limits) { l.LookupsPerRequest = 21 }},
		{"storage_lookups", func(l *Limits) { l.StorageLookups = 3 }},
		{"rulesets_per_project", func(l *Limits) { l.RulesetsPerProject = 2501 }},
	} {
		l := DefaultLimits()
		c.set(&l)
		err := l.Validate()
		if err == nil || !strings.Contains(err.Error(), c.name) || !strings.Contains(err.Error(), "Firebase") {
			t.Errorf("%s above Firebase's value: Validate() = %v", c.name, err)
		}
	}
}

func TestValidateAcceptsLoweredAndFreeLimits(t *testing.T) {
	l := Limits{
		CallDepth: 1, FunctionLets: 1, FunctionArgs: 1, ExpressionDepth: 1, MatchDepth: 1,
		MatchSegments: 1, MatchVariables: 1, SourceBytes: 1, Expressions: 1,
		LookupsPerOperation: 1, LookupsPerRequest: 1, StorageLookups: 1, RulesetsPerProject: 1,
		// Aruma's limits have no ceiling.
		MemberChain: 1 << 30, Work: 1 << 40, ValueBytes: 1 << 40, ValueElements: 1 << 30,
		AllocBytes: 1 << 40, InflightBytes: 1 << 40, RegexInstructions: 1 << 30,
		RequestSegments: 1 << 30, RegexCacheEntries: 1 << 30, RegexCacheBytes: 1 << 40, RegexBytes: 1 << 40,
		CompiledRulesetsBytes: 1 << 50,
	}
	if err := l.Validate(); err != nil {
		t.Errorf("lowered Firebase limits and raised Aruma ones: %v", err)
	}
}

func TestValidateRefusesLimitsBelowOne(t *testing.T) {
	err := Limits{}.Validate()
	if err == nil {
		t.Fatal("zero limits validate")
	}
	// Every limit is named, so a configuration error points at each key.
	for _, name := range []string{
		"call_depth", "function_lets", "function_args", "expression_depth", "match_depth",
		"match_segments", "match_variables", "source_bytes", "expressions",
		"lookups_per_operation", "lookups_per_request", "storage_lookups", "rulesets_per_project",
		"member_chain", "work", "value_bytes", "value_elements", "alloc_bytes", "inflight_bytes",
		"regex_instructions", "request_segments", "regex_cache_entries", "regex_cache_bytes",
		"regex_bytes", "compiled_rulesets_bytes",
	} {
		if !strings.Contains(err.Error(), name+" must be at least 1") {
			t.Errorf("Validate() of zero limits does not name %s: %v", name, err)
		}
	}
	l := DefaultLimits()
	l.Work = -1
	if l.Validate() == nil {
		t.Error("a negative work limit validates")
	}
}

func TestValidateRefusesInconsistentMemoryLimits(t *testing.T) {
	l := DefaultLimits()
	l.InflightBytes = l.AllocBytes - 1
	if err := l.Validate(); err == nil || !strings.Contains(err.Error(), "inflight_bytes") {
		t.Errorf("inflight_bytes below alloc_bytes: Validate() = %v", err)
	}
	l = DefaultLimits()
	l.ValueBytes = l.AllocBytes + 1
	if err := l.Validate(); err == nil || !strings.Contains(err.Error(), "value_bytes") {
		t.Errorf("value_bytes above alloc_bytes: Validate() = %v", err)
	}
	l = DefaultLimits()
	l.InflightBytes, l.ValueBytes = l.AllocBytes, l.AllocBytes
	if err := l.Validate(); err != nil {
		t.Errorf("inflight_bytes and value_bytes equal to alloc_bytes: %v", err)
	}
}

// A ruleset may weigh a quarter of the cache of compiled rulesets: limits under which one whose
// patterns use regex_bytes would weigh more do not validate, so the cache can always keep what
// compiles (the review's SR3-02).
func TestValidateRefusesACacheTooSmallForARuleset(t *testing.T) {
	l := DefaultLimits()
	need := int64(l.SourceBytes) + l.RegexCacheBytes + literalWeight(l.RegexBytes)
	l.CompiledRulesetsBytes = need * rulesetShare
	if err := l.Validate(); err != nil {
		t.Errorf("a cache of %d for rulesets of %d: %v", l.CompiledRulesetsBytes, need, err)
	}
	l.CompiledRulesetsBytes--
	if err := l.Validate(); err == nil || !strings.Contains(err.Error(), "compiled_rulesets_bytes") {
		t.Errorf("a cache too small: Validate() = %v", err)
	}
	l = DefaultLimits()
	l.RegexBytes = 1 << 62 // saturates rather than overflows
	if err := l.Validate(); err == nil || !strings.Contains(err.Error(), "compiled_rulesets_bytes") {
		t.Errorf("regex_bytes of 2^62: Validate() = %v", err)
	}
}
