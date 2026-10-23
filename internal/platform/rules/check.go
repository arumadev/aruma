package rules

// check checks a parsed file for svc and builds its ruleset: lexical scopes, the binding of
// every identifier and call, cycles and depth on the call graph, let in rules_version 1, the
// static limits, the warnings, the lookups each service enables, and the flattened match
// patterns. The ruleset is nil when any issue is an error. S1-C replaces this stub.
func check(f *file, svc Service, lim Limits) (*Ruleset, []Issue) {
	panic("S1-C: check")
}
