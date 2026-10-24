package rules

// The match blocks of a ruleset, flattened when the file compiles (ADR-021 §5.6): each block
// that holds allow statements becomes one pattern of segments from the service root, its own
// after those of every block around it, with a slot for each path variable. A request matches a
// block when the pattern takes its whole path; a block that takes part of it grants nothing by
// itself (rules do not cascade).
//
// A {x=**} takes a run of segments: one or more in rules_version 1, where it may only be the
// last segment of its match statement, and zero or more in version 2, where it may stand
// anywhere. Nesting is not concatenation (S0-A, more than 50 probes on the Rules Test API and
// the emulator, which agree on every one, and Pyric's hierarchical-match-cascade):
//
//   - a block nested in one whose own statement has a {x=**} before another segment never
//     matches (/{p=**}/groups/{g} > /members/{m} denies, while the same path written flat in
//     one statement grants); it compiles without a warning, and nothing in it is evaluated;
//   - a block nested in one whose own statement ends in {x=**} matches at any depth in version
//     2, and never in version 1 (or without a version line);
//   - a second {x=**} along one path, in a nested block, does not compile in version 2; in
//     version 1 it compiles with a warning, the nested block being dead.
//
// So a live pattern holds at most one {x=**}, and matching is linear: the segments before it take
// the start of the path, those after it the end, and it takes the run between. Its binding is
// fixed by the length; no backtracking or table is needed whatever the nesting.

// matchSeg is one segment of a flattened pattern.
type matchSeg struct {
	kind patternKind
	text string // the literal, or the variable's name
	slot int    // the variable's slot; unused for a literal
}

// compiledBlock is a match block with allow statements, flattened.
type compiledBlock struct {
	segs  []matchSeg
	globs int  // the {x=**} among segs: 0 or 1 in a ruleset that compiles
	nvars int  // the path variables of segs, which have slots 0 to nvars-1
	dead  bool // nested where it can match nothing
	// allows are the block's allow statements in source order; methods, what they cover
	// together, so that a block with nothing for a request's method is not matched at all.
	allows  []compiledAllow
	methods methodSet
}

// compiledAllow is an allow statement and the methods it covers.
type compiledAllow struct {
	a       *allow
	methods methodSet
	names   []string // the methods as written, for the trace
}

// methodSet is a set of Methods, one bit each.
type methodSet uint8

func (s methodSet) has(m Method) bool { return m >= MethodGet && m <= MethodDelete && s&(1<<m) != 0 }

// methodsNamed returns the methods an allow statement's method name covers: read is get and
// list, write is create, update and delete. ok is false for a name that is none of them, which
// the checker warns about and which covers nothing.
func methodsNamed(name string) (s methodSet, ok bool) {
	bit := func(ms ...Method) methodSet {
		var s methodSet
		for _, m := range ms {
			s |= 1 << m
		}
		return s
	}
	switch name {
	case "read":
		return bit(MethodGet, MethodList), true
	case "write":
		return bit(MethodCreate, MethodUpdate, MethodDelete), true
	case "get":
		return bit(MethodGet), true
	case "list":
		return bit(MethodList), true
	case "create":
		return bit(MethodCreate), true
	case "update":
		return bit(MethodUpdate), true
	case "delete":
		return bit(MethodDelete), true
	}
	return 0, false
}

// binding is the value of a path variable for one request. A variable that took an unknown
// segment has none (ok false): reading it is an error without a message (ADR-021 §2.4). A
// {x=**} keeps the run it took as a part of the request's path (run); its Path value is built,
// and charged to the evaluation, only when the condition reads it (evaluator.pathVar).
type binding struct {
	v    Value
	ok   bool
	run  []Segment
	lazy bool // a {x=**} whose Path is not built yet
}

// match reports whether the block's pattern takes the whole path, and what its variables hold,
// in vars, which it reuses (one evaluation tries block after block). It is linear in the
// pattern and builds nothing: matching thousands of blocks against a long path costs no memory.
func (b *compiledBlock) match(path []Segment, version int, vars []binding) ([]binding, bool) {
	if b.dead || b.globs > 1 { // more than one {x=**} does not compile; refuse it all the same
		return nil, false
	}
	n, m := len(b.segs), len(path)
	minRun := 0
	if version == 1 {
		minRun = 1
	}
	if (b.globs == 0 && m != n) || (b.globs == 1 && m < n-1+minRun) {
		return nil, false
	}
	run := m - n + 1 // the segments the {x=**} takes, when there is one
	// where returns the part of the path that segment s, placed at at, takes.
	where := func(s matchSeg, at int) (int, int) {
		if s.kind == patternRest {
			return at, at + run
		}
		return at, at + 1
	}
	at := 0
	for _, s := range b.segs {
		from, to := where(s, at)
		if s.kind != patternRest && !takes(s, path[from]) {
			return nil, false
		}
		at = to
	}
	vars = append(vars[:0], make([]binding, b.nvars)...)
	at = 0
	for _, s := range b.segs {
		from, to := where(s, at)
		bind(vars, s, path[from:to])
		at = to
	}
	return vars, true
}

// takes reports whether a literal or a {x} segment takes one segment of the path: a literal
// only its own text, a {x} any segment but a run of unknown length.
func takes(s matchSeg, p Segment) bool {
	switch s.kind {
	case patternLiteral:
		return p.kind == segmentLiteral && p.name == s.text
	case patternVariable:
		return p.kind == segmentLiteral || p.kind == segmentUnknown
	}
	return false
}

// bind sets the variable of s, if it has one, to the segments it took: a string for {x}, the
// run for {x=**}, and no value when one of them is unknown.
func bind(vars []binding, s matchSeg, took []Segment) {
	if s.kind == patternLiteral {
		return
	}
	for _, p := range took {
		if p.kind != segmentLiteral {
			vars[s.slot] = binding{}
			return
		}
	}
	if s.kind == patternVariable {
		vars[s.slot] = binding{v: String(took[0].name), ok: true}
		return
	}
	vars[s.slot] = binding{ok: true, run: took, lazy: true}
}

// globPath builds the Path value of a {x=**} run, charging it to m.
func globPath(m *meter, run []Segment) (Value, error) {
	if err := m.value(0, int64(len(run))); err != nil {
		return Value{}, err
	}
	if err := m.alloc(int64(len(run))*16 + 24); err != nil {
		return Value{}, err
	}
	names := make([]string, len(run))
	for i, p := range run {
		names[i] = p.name
	}
	return Value{kind: KindPath, x: names}, nil
}
