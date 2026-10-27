package rules

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// The checker's messages, as the emulator words them (hidane, rules_compile.json; production
// where S0-A measures otherwise). The shared ones are in issues.go.
const (
	msgOldVersion        = "Ruleset uses old version (version [1]). Please update to the latest version (version [2])."
	msgUnsupportedVer    = "Unsupported rules version %s."
	msgInvalidService    = "Invalid service name: %s."
	msgInvalidMethod     = "Invalid method name: %s."
	msgInvalidFunction   = "Invalid function name: %s."
	msgInvalidVariable   = "Invalid variable name: %s."
	msgUnusedFunction    = "Unused function: %s."
	msgUnusedVariable    = "Unused variable: %s."
	msgArity             = "Incorrect number of arguments supplied to function: %s."
	msgPackage           = "%s is a package and cannot be used as variable name."
	msgFunctionDefined   = "Function %s is already defined."
	msgParamDefined      = "Variable %s is already defined."
	msgAlreadyBound      = "The identifier [%s] is already bound in the current scope."
	msgRecursive         = "Recursive call is not allowed."
	msgCycle             = "Detected a call cycle between [%s] functions."
	msgTooManyArgs       = "Maximum allowed function arguments of %d for function %s is reached."
	msgTooManyLets       = "Maximum allowed variable count of %d for a given function has been reached."
	msgMatchDepth        = "Maximum allowed resource rule depth of %d is reached."
	msgMatchSegments     = "Maximum allowed path length of %d is reached."
	msgMatchVariables    = "Maximum allowed amount of %d match field variables is reached."
	msgHierarchy         = "Rule hierarchy is too complex to evaluate safely."
	msgGlobLast          = "Invalid glob match expression. Glob matches are permitted as the last segment in a match declaration path."
	msgGlobOnce          = "Invalid glob match expression. Only one glob match is permitted in a match declaration path."
	msgGlobChildren      = "Glob(**) resource rules should not have child resource rules."
	msgVariableInPattern = "Variable %s already exists in the PathTemplate."
	msgSourceTooLarge    = "Total size of sources must be smaller than %d."
	// Aruma's, for its limits (ADR-021 §4).
	msgRulesetWeight = "The rules are too large to keep compiled: they would take about %d bytes, more than the %d one ruleset may."
)

// hierarchyLimit is how deep match blocks may nest before the checker stops descending, as the
// emulator does past 63 ("Rule hierarchy is too complex to evaluate safely."). The limit of
// Firebase, lim.MatchDepth, is reported long before.
const hierarchyLimit = 64

// nodeBytes estimates what one node of the tree holds, for Weight.
const nodeBytes = 96

// check checks a parsed file for svc and builds its ruleset: lexical scopes, the binding of
// every identifier and call, cycles and depth on the call graph, let in rules_version 1, the
// static limits, the warnings, the lookups each service enables, and the flattened match
// patterns. The ruleset is nil when any issue is an error.
//
// It runs on whatever tree the parser recovered, so every part of the tree may be missing.
func check(f *file, svc Service, lim Limits) (*Ruleset, []Issue) {
	c := &checker{f: f, svc: svc, lim: &lim, info: map[*function]*fnInfo{}}
	c.rs = &Ruleset{service: svc, lim: lim, name: f.name, src: f.src}
	c.rs.regexes = newRegexCache(&c.rs.lim)
	c.versions()
	c.rs.version = c.version
	// A file for the other service is an error (ADR-021 §2.1). A name that is no service compiles
	// with a warning, as in production, and the ruleset grants nothing.
	known := f.service == string(Firestore) || f.service == string(Storage)
	switch {
	case !known:
		c.warnf(f.serviceAt, msgInvalidService, f.service)
	case f.service != string(svc):
		c.errorf(f.serviceAt, msgInvalidService, f.service)
	}

	top, topFns := c.declare(nil, f.functions)
	service, serviceFns := c.declare(top, f.body.functions)
	c.bodies(topFns)
	c.bodies(serviceFns)
	for _, m := range f.body.matches {
		c.match(service, m, matchState{})
	}
	c.calls()
	for _, fi := range c.fns {
		if !fi.used {
			c.warnf(fi.fn.name.span, msgUnusedFunction, fi.fn.name.text)
		}
	}
	c.rs.treeBytes = c.nodes * nodeBytes
	if !known {
		c.rs.blocks = nil
	}
	// A ruleset the cache could not keep would be compiled again for every request that needs
	// it (the review's SR3-02): it does not compile. Its patterns are bounded already
	// (regex_bytes); what is left is the text and the tree, by its estimate of 96 bytes a node
	// (the oracle's largest tree, size_functions_950, is 1.3 MB).
	if w := c.rs.Weight(); w > c.lim.rulesetBytes() {
		c.issues = append(c.issues, Issue{File: f.name, Severity: SeverityError, Description: fmt.Sprintf(msgRulesetWeight, w, c.lim.rulesetBytes())})
	}

	c.positions()
	for _, is := range c.issues {
		if is.Severity == SeverityError {
			return nil, c.issues
		}
	}
	return c.rs, c.issues
}

// checker is the state of one check.
type checker struct {
	f       *file
	svc     Service
	lim     *Limits
	version int
	rs      *Ruleset
	issues  []Issue
	spans   []issueSpan // the issues still without their Position, filled by positions

	fns   []*fnInfo // every function, in the order declared
	info  map[*function]*fnInfo
	nodes int64 // expressions seen, for Weight
	// regexFull is set once a literal pattern is past regex_bytes: the patterns after it are
	// not compiled, so the check of a hostile file stops building programs at the budget.
	regexFull bool
}

// fnInfo is what the checker knows of a function.
type fnInfo struct {
	fn        *function
	sc        *scope // where it is declared, and so what it sees
	used      bool
	calls     []*function // the functions its body calls, in order
	paramUsed []bool
	letUsed   []bool
}

// scope is a level of names: the file, the service, or a match block with its own path
// variables. A name resolves in the innermost scope that has it.
type scope struct {
	parent    *scope
	functions map[string]*function
	vars      []pathVar
}

type pathVar struct {
	name string
	slot int
}

// function returns the function name resolves to from sc, or nil.
func (sc *scope) function(name string) *function {
	for ; sc != nil; sc = sc.parent {
		if f := sc.functions[name]; f != nil {
			return f
		}
	}
	return nil
}

// pathVar returns the slot of the path variable name resolves to from sc.
func (sc *scope) pathVar(name string) (int, bool) {
	for ; sc != nil; sc = sc.parent {
		for i := len(sc.vars) - 1; i >= 0; i-- {
			if sc.vars[i].name == name {
				return sc.vars[i].slot, true
			}
		}
	}
	return 0, false
}

func (c *checker) errorf(at span, format string, args ...any) {
	c.issue(at, SeverityError, fmt.Sprintf(format, args...))
}

func (c *checker) warnf(at span, format string, args ...any) {
	c.issue(at, SeverityWarning, fmt.Sprintf(format, args...))
}

func (c *checker) issue(at span, sev Severity, desc string) {
	c.spans = append(c.spans, issueSpan{i: len(c.issues), at: at})
	c.issues = append(c.issues, Issue{File: c.f.name, Position: new(Position), Severity: sev, Description: desc})
}

// issueSpan is where an issue of c.issues lies, until positions converts it.
type issueSpan struct {
	i  int
	at span
}

// positions gives every issue its Position in one pass over the source: the spans in order of
// their start, through one posCursor, so a check with n issues costs O(n log n) and one scan,
// not a scan from the start per issue. Each Position is what position(src, at) returns.
func (c *checker) positions() {
	slices.SortStableFunc(c.spans, func(a, b issueSpan) int { return cmp.Compare(a.at.start, b.at.start) })
	cur := newPosCursor(c.f.src)
	for _, s := range c.spans {
		*c.issues[s.i].Position = cur.at(s.at)
	}
	c.spans = nil
}

// versions reads rules_version: none or '1' is version 1, warned about as old; '2' is 2; any
// other is an error, and the file is checked as version 1.
func (c *checker) versions() {
	c.version = 1
	switch c.f.version {
	case "", "1":
		c.issues = append(c.issues, Issue{File: c.f.name, Severity: SeverityWarning, Description: msgOldVersion})
	case "2":
		c.version = 2
	default:
		c.errorf(c.f.versionAt, msgUnsupportedVer, c.f.version)
	}
}

// declare makes the scope of a level's functions. Their bodies are checked once the scope is
// complete (bodies), so that a function sees every function of its level whatever their order.
func (c *checker) declare(parent *scope, fns []*function) (*scope, []*fnInfo) {
	sc := &scope{parent: parent, functions: map[string]*function{}}
	var declared []*fnInfo
	for _, fn := range fns {
		if fn == nil {
			continue
		}
		if sc.functions[fn.name.text] != nil {
			c.errorf(fn.name.span, msgFunctionDefined, fn.name.text)
			continue
		}
		sc.functions[fn.name.text] = fn
		fi := &fnInfo{fn: fn, sc: sc, paramUsed: make([]bool, len(fn.params)), letUsed: make([]bool, len(fn.lets))}
		c.info[fn] = fi
		c.fns = append(c.fns, fi)
		declared = append(declared, fi)
	}
	return sc, declared
}

// bodies checks the functions of one level.
func (c *checker) bodies(fns []*fnInfo) {
	for _, fi := range fns {
		c.body(fi)
	}
}

// body checks a function: its parameters and lets, their limits and names, and the expressions
// that use them.
func (c *checker) body(fi *fnInfo) {
	fn := fi.fn
	if len(fn.params) > c.lim.FunctionArgs {
		c.errorf(fn.name.span, msgTooManyArgs, c.lim.FunctionArgs, fn.name.text)
	}
	for i, p := range fn.params {
		for _, q := range fn.params[:i] {
			if q.text == p.text {
				c.errorf(p.span, msgParamDefined, p.text)
				break
			}
		}
	}
	// Production (the Rules Test API, S0-A) and the emulator compile one let more than the limit
	// they name: 11 compile, 12 fail "of 10". The error stands at what follows the twelfth let:
	// the thirteenth let, or the return's expression when there is none (aruma/limits/lets_12 at
	// L4:177, lets_21 at L4:170, on both).
	if len(fn.lets) > c.lim.FunctionLets+1 {
		at := fn.span
		switch next := c.lim.FunctionLets + 2; {
		case next < len(fn.lets):
			at = fn.lets[next].span
		case fn.ret != nil:
			at = fn.ret.pos()
		}
		c.errorf(at, msgTooManyLets, c.lim.FunctionLets)
	}
	for i, l := range fn.lets {
		if l == nil {
			continue
		}
		if c.version == 1 {
			c.errorf(l.name.span, msgLetVersion1)
		}
		bound := slices.ContainsFunc(fn.params, func(p name) bool { return p.text == l.name.text }) ||
			slices.ContainsFunc(fn.lets[:i], func(o *let) bool { return o != nil && o.name.text == l.name.text })
		if bound {
			c.errorf(l.name.span, msgAlreadyBound, l.name.text)
		}
		c.expr(l.value, exprCtx{sc: fi.sc, fn: fi, lets: i})
	}
	c.expr(fn.ret, exprCtx{sc: fi.sc, fn: fi, lets: len(fn.lets)})
	for i, p := range fn.params {
		if !fi.paramUsed[i] {
			c.warnf(p.span, msgUnusedVariable, p.text)
		}
	}
	for i, l := range fn.lets {
		if l != nil && !fi.letUsed[i] {
			c.warnf(l.name.span, msgUnusedVariable, l.name.text)
		}
	}
}

// matchState is what the blocks around a match block contribute to it.
type matchState struct {
	segs       []matchSeg
	nvars      int
	globs      int
	dead       bool
	depth      int // match blocks around it
	segments   int // their path segments, all together
	long, many bool
}

// match checks a match block and the blocks in it, and flattens those with allow statements.
func (c *checker) match(parent *scope, m *match, st matchState) {
	if m == nil {
		return
	}
	st.depth++
	if st.depth >= hierarchyLimit {
		c.errorf(m.span, msgHierarchy)
		return
	}
	// The emulator counts the first block of a service at depth 0, so MatchDepth+1 nest.
	if st.depth == c.lim.MatchDepth+2 {
		c.errorf(m.span, msgMatchDepth, c.lim.MatchDepth)
	}
	st.segments += len(m.path)
	if st.segments > c.lim.MatchSegments && !st.long {
		st.long = true
		c.errorf(m.span, msgMatchSegments, c.lim.MatchSegments)
	}

	segs := st.segs[:len(st.segs):len(st.segs)] // a copy on the first append
	var vars []pathVar
	globs, nonFinal := 0, false
	for i, p := range m.path {
		s := matchSeg{kind: p.kind, text: p.text}
		if p.kind == patternRest {
			// In version 1, last in its statement; a second one nested in a block compiles
			// there (production warns only of the children), the nested block being dead. In
			// version 2, one along a path, nested blocks included.
			switch {
			case c.version == 1 && i != len(m.path)-1:
				c.errorf(span{segmentStart(c.f.src, p.span), p.end}, msgGlobLast)
			case c.version == 2 && st.globs+globs > 0:
				at := m.span
				if globs > 0 {
					at = span{segmentStart(c.f.src, p.span), p.end}
				}
				c.errorf(at, msgGlobOnce)
			}
			globs++
			nonFinal = nonFinal || i != len(m.path)-1
		}
		if p.kind != patternLiteral {
			if slices.ContainsFunc(vars, func(v pathVar) bool { return v.name == p.text }) {
				c.errorf(nameIn(c.f.src, p.span, p.text), msgVariableInPattern, p.text)
			}
			s.slot = st.nvars
			vars = append(vars, pathVar{name: p.text, slot: st.nvars})
			st.nvars++
		}
		segs = append(segs, s)
	}
	if st.nvars > c.lim.MatchVariables && !st.many {
		st.many = true
		c.errorf(m.span, msgMatchVariables, c.lim.MatchVariables)
	}
	if c.version == 1 && globs > 0 && len(m.body.matches) > 0 {
		c.warnf(m.span, msgGlobChildren)
	}

	// The block's variables are in scope for its functions, its conditions and the blocks in it.
	declared, fns := c.declare(parent, m.body.functions)
	declared.vars = vars
	c.bodies(fns)

	if len(m.body.allows) > 0 {
		b := &compiledBlock{segs: segs, globs: st.globs + globs, nvars: st.nvars, dead: st.dead}
		for _, a := range m.body.allows {
			if a == nil {
				continue
			}
			ca := compiledAllow{a: a}
			for _, n := range a.methods {
				ms, ok := methodsNamed(n.text)
				if !ok {
					c.warnf(n.span, msgInvalidMethod, n.text)
				}
				ca.methods |= ms
				ca.names = append(ca.names, n.text)
			}
			c.expr(a.cond, exprCtx{sc: declared})
			b.allows = append(b.allows, ca)
			b.methods |= ca.methods
		}
		c.rs.blocks = append(c.rs.blocks, b)
	}

	st.segs = segs
	st.globs += globs
	// Blocks nested under a non-final {x=**} never match; under a final one, only in version 2.
	st.dead = st.dead || nonFinal || (c.version == 1 && globs > 0)
	for _, inner := range m.body.matches {
		c.match(declared, inner, st)
	}
}

// segmentStart returns where a path segment starts with its slash, which the emulator reports
// glob errors at, whether or not the parser's span of the segment holds the slash.
func segmentStart(src string, at span) int {
	switch {
	case at.start < len(src) && at.start >= 0 && src[at.start] == '/':
		return at.start
	case at.start > 0 && at.start <= len(src) && src[at.start-1] == '/':
		return at.start - 1
	}
	return at.start
}

// nameIn returns the span of a variable's name in its segment's span, where the emulator
// reports a repeated variable.
func nameIn(src string, at span, text string) span {
	if at.start >= 0 && at.end <= len(src) && at.start <= at.end {
		if i := strings.Index(src[at.start:at.end], text); i >= 0 {
			return span{at.start + i, at.start + i + len(text)}
		}
	}
	return at
}

// exprCtx is where an expression is checked: the scope of names, and in a function the
// function and how many of its lets are visible (a let sees those before it).
type exprCtx struct {
	sc   *scope
	fn   *fnInfo
	lets int
}

// expr resolves the identifiers and calls of e.
func (c *checker) expr(e expr, cx exprCtx) {
	if e == nil {
		return
	}
	c.nodes++
	switch e := e.(type) {
	case *litExpr:
	case *identExpr:
		c.ident(e, cx)
		c.noteRequest(e, "")
	case *memberExpr:
		c.operand(e.x, cx, e.name)
	case *indexExpr:
		field := ""
		if l, ok := e.i.(*litExpr); ok {
			field, _ = l.val.AsString()
		}
		c.operand(e.x, cx, field)
		c.expr(e.i, cx)
	case *sliceExpr:
		c.expr(e.x, cx)
		c.expr(e.lo, cx)
		c.expr(e.hi, cx)
	case *callExpr:
		c.call(e, cx)
	case *unaryExpr:
		c.expr(e.x, cx)
	case *binaryExpr:
		c.expr(e.x, cx)
		c.expr(e.y, cx)
	case *isExpr:
		c.expr(e.x, cx)
	case *condExpr:
		c.expr(e.test, cx)
		c.expr(e.yes, cx)
		c.expr(e.no, cx)
	case *listExpr:
		for _, x := range e.elems {
			c.expr(x, cx)
		}
	case *mapExpr:
		for _, x := range e.keys {
			c.expr(x, cx)
		}
		for _, x := range e.vals {
			c.expr(x, cx)
		}
	case *pathExpr:
		for _, s := range e.segs {
			c.expr(s.x, cx)
		}
	}
}

// operand checks x, the operand of a member or an index named field ("" when it is not a
// literal string).
func (c *checker) operand(x expr, cx exprCtx, field string) {
	id, ok := x.(*identExpr)
	if !ok {
		c.expr(x, cx)
		return
	}
	c.nodes++
	c.ident(id, cx)
	c.noteRequest(id, field)
}

// noteRequest marks the ruleset as reading request.time when id is request and field is time,
// or is not known ("": request as a whole, or indexed by an expression), for ReadsRequestTime.
func (c *checker) noteRequest(id *identExpr, field string) {
	if id.kind == identGlobal && id.name == "request" && (field == "" || field == "time") {
		c.rs.readsTime = true
	}
}

// ident resolves a name: a let or parameter of the function, a path variable of the blocks
// around, then request and resource. A namespace is not a variable (an error); any other name
// is warned about and is an error when evaluated.
func (c *checker) ident(e *identExpr, cx exprCtx) {
	e.kind, e.slot = identUnresolved, 0
	if fi := cx.fn; fi != nil {
		for i := min(cx.lets, len(fi.fn.lets)) - 1; i >= 0; i-- {
			if l := fi.fn.lets[i]; l != nil && l.name.text == e.name {
				e.kind, e.slot = identLet, i
				fi.letUsed[i] = true
				return
			}
		}
		for i := len(fi.fn.params) - 1; i >= 0; i-- {
			if fi.fn.params[i].text == e.name {
				e.kind, e.slot = identParam, i
				fi.paramUsed[i] = true
				return
			}
		}
	}
	if slot, ok := cx.sc.pathVar(e.name); ok {
		e.kind, e.slot = identPathVar, slot
		return
	}
	switch {
	case isGlobal(e.name):
		e.kind = identGlobal
	case c.isNamespace(e.name):
		e.kind = identNamespace
		c.errorf(e.span, msgPackage, e.name)
	default:
		c.warnf(e.span, msgInvalidVariable, e.name)
	}
}

// isGlobal reports whether name is a variable the service supplies.
func isGlobal(name string) bool { return name == "request" || name == "resource" }

// isNamespace reports whether name is a namespace of functions in the checker's service: the
// library's, and firestore in Storage.
func (c *checker) isNamespace(name string) bool {
	return isLibraryNamespace(name) || (c.svc == Storage && name == "firestore")
}

// call resolves a call: f() to a function of the file (the innermost scope first), a lookup the
// service enables, or a global library function; ns.f() to a namespaced function or lookup;
// x.f() to a method of x. Anything else is warned about and fails when evaluated.
func (c *checker) call(e *callExpr, cx exprCtx) {
	e.kind, e.user, e.name = callsUnresolved, nil, ""
	switch fn := e.fn.(type) {
	case *identExpr:
		c.nodes++
		e.name = fn.name
		switch user := cx.sc.function(fn.name); {
		case user != nil:
			e.kind, e.user = callsUser, user
			if fi := c.info[user]; fi != nil {
				fi.used = true
			}
			if cx.fn != nil {
				cx.fn.calls = append(cx.fn.calls, user)
			}
			if len(e.args) != len(user.params) {
				c.warnf(fn.span, msgArity, fn.name)
			}
		case isLookup(c.svc, fn.name):
			e.kind = callsLookup
		case isLibraryFunction(fn.name):
			e.kind = callsFunction
			c.libraryArity(fn.span, fn.name, len(e.args))
		default:
			c.warnf(fn.span, msgInvalidFunction, fn.name)
		}
	case *memberExpr:
		c.nodes++
		if ns, ok := fn.x.(*identExpr); ok && c.isNamespace(ns.name) {
			c.nodes++
			ns.kind = identNamespace
			e.name = ns.name + "." + fn.name
			switch {
			case isLookup(c.svc, e.name):
				e.kind = callsLookup
			case isLibraryFunction(e.name):
				e.kind = callsFunction
				c.libraryArity(fn.nameAt, e.name, len(e.args))
			default:
				c.warnf(fn.nameAt, msgInvalidFunction, e.name)
			}
			break
		}
		e.kind, e.name = callsMethod, fn.name
		c.methodArity(fn.nameAt, fn.name, len(e.args))
		c.expr(fn.x, cx)
		c.literalRegex(e)
	default:
		c.expr(e.fn, cx)
	}
	for _, a := range e.args {
		c.expr(a, cx)
	}
}

// libraryArities and methodArities are the functions and methods whose number of arguments
// production checks when it compiles, with a warning at the name; the call is still made when
// evaluated. Only those measured on the Rules Test API are listed (S1-E probe arity.mjs,
// 2026-10-24): debug(1, 2) and debug() warn (and deployed, debug(1, 2) is 1 and debug() an
// error: callFunction); math.abs(1, 2) warns at abs; 'a'.size(1), [1].size(1) and
// 'a'.lower(1) warn at the method, then fail "Incorrect number of arguments". A method is
// listed only where every receiver that has it takes the same number, so the warning needs no
// type of the receiver. Production warns about other calls the checker does not know
// (docs/compat/rules-v0.3.md).
var (
	libraryArities = map[string]int{"debug": 1, "math.abs": 1}
	methodArities  = map[string]int{"size": 0, "lower": 0}
)

// libraryArity warns about a call of the global function name with n arguments, as production
// does where it is measured.
func (c *checker) libraryArity(at span, name string, n int) {
	if want, ok := libraryArities[name]; ok && n != want {
		c.warnf(at, msgArity, name)
	}
}

// methodArity warns about a call of the method name with n arguments, as production does where
// it is measured.
func (c *checker) methodArity(at span, name string, n int) {
	if want, ok := methodArities[name]; ok && n != want {
		c.warnf(at, msgArity, name)
	}
}

// literalRegex compiles the pattern a string literal passes to matches, split or replace, so
// that an invalid one, or one whose program is too large, fails compilation; an invalid one of
// replace() fails only when evaluated (below). So does the first pattern that takes the file's
// patterns past regex_bytes, at its literal, and the patterns after it are not compiled.
func (c *checker) literalRegex(e *callExpr) {
	if c.regexFull {
		return
	}
	switch e.name {
	case "matches", "split", "replace":
	default:
		return
	}
	if len(e.args) == 0 {
		return
	}
	lit, ok := e.args[0].(*litExpr)
	if !ok {
		return
	}
	if pattern, ok := lit.val.AsString(); ok {
		// Production refuses an invalid literal of matches() and split() when it compiles, but
		// one of replace() only when the call is evaluated, an error || absorbs (Rules Test API,
		// S1-E probe 2026-10-24: '(' and '(?=b)' fail to compile in matches and split; '(' and
		// '(a)\1' in replace give "Invalid regular expression pattern" at the call). A valid
		// literal of replace() is still compiled once, here, and one past Aruma's limits (too
		// many instructions) fails compilation whatever the method: that is our limit, not
		// production's syntax.
		err := compileLiteralRegex(c.rs.regexes, pattern)
		if err != nil && !(e.name == "replace" && err.Error() == errRegex(pattern).Error()) {
			c.issue(lit.span, SeverityError, err.Error())
		}
		if err != nil && err.Error() == errRegexBudget(pattern, c.lim.RegexBytes).Error() {
			c.regexFull = true
		}
	}
}

// What the errors of the call graph name, so that their text stays linear in the source: a
// file of 6,200 functions each calling f0 and the next made 6,200 errors naming the whole
// stack, 104 MiB of text (the review's SR3-04).
const (
	// maxCycleNames is how many functions an error of a cycle names; it counts the others.
	maxCycleNames = 32
	// maxCycleErrors is how many cycles a check reports: the file fails at the first.
	maxCycleErrors = 100
)

// calls checks the call graph: a function that calls itself, a cycle of calls, and a chain of
// calls longer than the limit, which are errors whether or not the functions are used. As the
// emulator measures (rules_compile.json, "calls 22 deep"), a chain of CallDepth+1 functions
// compiles; one more fails at the function past the limit, naming the stack that reached it.
//
// Each cycle is reported once, at the function the search found it back at, naming the stack
// from there (cycleNames); the search is linear in the calls.
func (c *checker) calls() {
	// Cycles, depth first from the functions in the order declared.
	const (
		fresh = iota
		onStack
		done
	)
	state := make(map[*function]int, len(c.fns))
	at := make(map[*function]int, len(c.fns)) // where a function on the stack is in it
	reported := map[*function]bool{}          // the functions a cycle was reported at
	var stack []*function
	cyclic := false
	var visit func(f *function)
	visit = func(f *function) {
		state[f] = onStack
		at[f] = len(stack)
		stack = append(stack, f)
		fi := c.info[f]
		for _, g := range uniq(fi.calls) {
			switch state[g] {
			case fresh:
				visit(g)
			case onStack:
				cyclic = true
				if reported[g] || len(reported) >= maxCycleErrors {
					continue
				}
				reported[g] = true
				cycle := stack[at[g]:]
				if len(cycle) == 1 {
					c.errorf(g.name.span, msgRecursive)
				} else {
					c.errorf(g.name.span, msgCycle, cycleNames(cycle))
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[f] = done
	}
	for _, fi := range c.fns {
		if state[fi.fn] == fresh {
			visit(fi.fn)
		}
	}
	if cyclic {
		return // reported above; a cycle has no depth
	}

	// The longest chain from each function, on the graph, which has no cycle now.
	longest := map[*function]int{}
	var chain func(f *function) int
	chain = func(f *function) int {
		if n, ok := longest[f]; ok {
			return n
		}
		n := 1
		for _, g := range c.info[f].calls {
			n = max(n, 1+chain(g))
		}
		longest[f] = n
		return n
	}
	allowed := c.lim.CallDepth + 1
	for _, fi := range c.fns {
		if chain(fi.fn) <= allowed {
			continue
		}
		// Follow the deepest calls: the stack that reached the limit, and the function past it.
		var names []string
		f := fi.fn
		for len(names) < allowed {
			names = append(names, clip(f.name.text))
			next := c.info[f].calls[0]
			for _, g := range c.info[f].calls {
				if longest[g] > longest[next] {
					next = g
				}
			}
			f = next
		}
		c.errorf(f.name.span, msgCallDepth, c.lim.CallDepth, strings.Join(names, "->"))
		return
	}
}

// cycleNames lists the functions of a cycle for its error, comma-separated: all of them, or the
// first maxCycleNames and how many more.
func cycleNames(cycle []*function) string {
	var b strings.Builder
	for i, h := range cycle {
		if i == maxCycleNames {
			fmt.Fprintf(&b, " and %d more", len(cycle)-i)
			break
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(clip(h.name.text))
	}
	return b.String()
}

// uniq returns fs without repeats, in order.
func uniq(fs []*function) []*function {
	seen := make(map[*function]bool, len(fs))
	out := make([]*function, 0, len(fs))
	for _, f := range fs {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}
