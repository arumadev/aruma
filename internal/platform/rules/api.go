package rules

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
)

// Service names the service a rules file is for, as its service declaration does.
type Service string

const (
	Firestore Service = "cloud.firestore"
	Storage   Service = "firebase.storage"
)

// File is one rules source file. The Rules API accepts one file per ruleset (ADR-021 §2.1).
type File struct {
	Name    string // as the issues name it, e.g. "firestore.rules"
	Content string
}

// Compile parses and checks one rules file for a service. The issues hold every error and
// warning in source order; the ruleset is nil when any issue is an error.
//
// Limits that do not validate, a service other than Firestore and Storage, and sources too
// large (sourceTooLarge) are errors about the whole file: nothing compiles under them. A file
// with a syntax error is not checked: production reports the syntax errors alone.
func Compile(svc Service, f File, lim Limits) (*Ruleset, Issues) {
	whole := func(desc string) Issues {
		return Issues{{File: f.Name, Severity: SeverityError, Description: desc}}
	}
	if err := lim.Validate(); err != nil {
		return nil, whole("Invalid rules limits: " + err.Error())
	}
	if svc != Firestore && svc != Storage {
		return nil, whole(fmt.Sprintf(msgInvalidService, svc))
	}
	if sourceTooLarge(f, &lim) {
		return nil, whole(fmt.Sprintf(msgSourceTooLarge, lim.SourceBytes))
	}
	tree, issues := parse(f.Name, f.Content, lim)
	if tree == nil || Issues(issues).Err() != nil {
		return nil, sortIssues(issues)
	}
	tree.name, tree.src = f.Name, f.Content
	rs, more := check(tree, svc, lim)
	all := sortIssues(append(issues, more...))
	if all.Err() != nil {
		return nil, all
	}
	return rs, all
}

// sourceTooLarge reports whether a file's sources are past lim.SourceBytes as the Rules API
// counts "Total size of sources must be smaller than 262144.": the size of the Source message
// in protocol buffers, Source{files: [File{content: 1, name: 2}]}, may be the limit itself and
// no more. So the framing is 10 bytes for a name under 128 bytes, and 11 from 128, whose length
// takes two bytes: for firestore.rules 262,119 bytes of content compile and 262,120 do not
// (S0-A), and with names of 1, 100, 127, 128, 129 and 300 bytes the boundary moves by the
// name's length, and by one more byte from 128 (S1-E probe of the Rules Test API, 2026-10-24).
func sourceTooLarge(f File, lim *Limits) bool {
	// A length-delimited field of n bytes, and a string's, which proto3 omits when empty.
	field := func(n int) int { return 1 + varintLen(n) + n }
	str := func(s string) int {
		if s == "" {
			return 0
		}
		return field(len(s))
	}
	return field(str(f.Content)+str(f.Name)) > lim.SourceBytes
}

// varintLen is how many bytes protocol buffers write n in.
func varintLen(n int) int {
	l := 1
	for ; n >= 0x80; n >>= 7 {
		l++
	}
	return l
}

// sortIssues orders issues as the Rules API lists them: those about the whole file first, then
// by where they are.
func sortIssues(is []Issue) Issues {
	slices.SortStableFunc(is, func(a, b Issue) int {
		switch {
		case a.Position == nil && b.Position == nil:
			return 0
		case a.Position == nil:
			return -1
		case b.Position == nil:
			return 1
		}
		return cmp.Compare(a.Position.CurrentOffset, b.Position.CurrentOffset)
	})
	return is
}

// Ruleset is a compiled rules file. It never changes once built and is safe for concurrent
// use; the cache of patterns built during evaluation that it holds is guarded by a lock.
type Ruleset struct {
	version int // 1 or 2
	service Service
	lim     Limits
	name    string // the file's name, for positions
	src     string // the file's text, for positions

	// regexes caches the patterns built during evaluation, and holds the compiled literal
	// ones (regex.go).
	regexes *regexCache

	// blocks are the match blocks with allow statements, flattened, in file order (a block
	// before the blocks in it). The functions they call hang off the tree (callExpr.user).
	blocks []*compiledBlock
	// treeBytes estimates the tree the ruleset keeps, for Weight.
	treeBytes int64
	// readsTime is whether an expression may read request.time (ReadsRequestTime).
	readsTime bool
}

// Version returns the file's rules_version: 1 or 2.
func (rs *Ruleset) Version() int { return rs.version }

// RequestSegments returns the longest request path the ruleset evaluates (request_segments):
// Evaluate refuses a longer one as an invalid request, which a service answers UNAVAILABLE, so
// the service denies such a path itself.
func (rs *Ruleset) RequestSegments() int { return rs.lim.RequestSegments }

// ReadsRequestTime reports whether an expression of the file may read request.time, so that a
// decision may change with the clock alone. It is conservative: request used whole (passed to a
// function, compared, its keys()) or indexed by an expression counts as reading it.
func (rs *Ruleset) ReadsRequestTime() bool { return rs.readsTime }

// Service returns the service the ruleset was compiled for.
func (rs *Ruleset) Service() Service { return rs.service }

// Weight estimates the bytes the ruleset holds, for tenancy's cache of compiled rulesets, which
// is bounded by weight rather than count (ADR-021 §4, §7): its text, its tree, the programs of
// its literal patterns with the tables of their classes and the forms for lone surrogates they
// may keep, and the cap of its cache of patterns built during evaluation, which it may fill.
// Compile refuses a ruleset that would weigh more than a quarter of compiled_rulesets_bytes, so
// every ruleset that compiles is kept. It is an estimate: over the pattern shapes measured
// (\pL, \PL, \p{Han}, negated and case-folded classes, alternations of categories, repetitions,
// lone forms), a file whose patterns use regex_bytes holds at most 0.84 times what the budgets
// count, and with its lone forms at most its weight (TestClassTablesAreCounted); the tree's
// share is not measured that way.
func (rs *Ruleset) Weight() int64 {
	w := int64(len(rs.src)) + rs.treeBytes + rs.lim.RegexCacheBytes
	if rs.regexes != nil {
		w += literalWeight(rs.regexes.literalBytes())
	}
	return w
}

// Method is what a request does, as allow statements name it. read covers Get and List; write
// covers Create, Update and Delete.
type Method uint8

const (
	MethodGet Method = iota + 1
	MethodList
	MethodCreate
	MethodUpdate
	MethodDelete
)

func (m Method) String() string {
	switch m {
	case MethodGet:
		return "get"
	case MethodList:
		return "list"
	case MethodCreate:
		return "create"
	case MethodUpdate:
		return "update"
	case MethodDelete:
		return "delete"
	}
	return "invalid"
}

// Segment is one segment of a request path: a literal, or one of the two unknown segments a
// Firestore list query needs (ADR-021 §2.4). The zero Segment is the empty literal.
type Segment struct {
	name string
	kind segmentKind
}

type segmentKind uint8

const (
	segmentLiteral segmentKind = iota
	segmentUnknown
	segmentUnknownRun
)

var (
	// UnknownSegment is a segment whose value is not known, such as the document ID of a list
	// query. {x} and {x=**} take it, and leave x without a value: reading x is an error.
	UnknownSegment = Segment{kind: segmentUnknown}
	// UnknownRun is a run of segments of unknown length, such as what precedes the collection
	// of a collection group query. Only {x=**} takes it; a literal or {x} before it does not
	// match.
	UnknownRun = Segment{kind: segmentUnknownRun}
)

// Literal returns the literal segment name. The name is used as given: a service splits an
// object name on "/" and passes each part, without decoding.
func Literal(name string) Segment { return Segment{name: name} }

// Literals returns a literal segment for each name.
func Literals(names ...string) []Segment {
	segs := make([]Segment, len(names))
	for i, n := range names {
		segs[i] = Literal(n)
	}
	return segs
}

// Name returns the segment's text, and false for the unknown segments.
func (s Segment) Name() (string, bool) { return s.name, s.kind == segmentLiteral }

func (s Segment) String() string {
	switch s.kind {
	case segmentUnknown:
		return "<unknown>"
	case segmentUnknownRun:
		return "<unknown run>"
	}
	return s.name
}

// Request is one operation to decide.
type Request struct {
	Project   int64            // the project whose documents lookups read
	Method    Method           // MethodGet, MethodList, MethodCreate, MethodUpdate, MethodDelete
	Path      []Segment        // what the match blocks see, from the service root
	Variables map[string]Value // "request" and "resource", built by the service
	Documents Documents        // required; rules.NoDocuments{} where Firestore cannot be read
	Budget    *Budget          // the request's lookup count and cache, one per request, shared by a batch
	Inflight  *Inflight        // required; the process-wide cap on evaluation memory, made once by app
	Trace     TraceLevel       // TraceNone or TraceStatements; TraceExpressions is reserved for :test (S5)
}

// TraceLevel selects what a Decision records beside the verdict.
type TraceLevel uint8

const (
	TraceNone       TraceLevel = iota
	TraceStatements            // every allow statement evaluated, with its outcome
	// TraceExpressions is reserved for the Rules API's projects.test report (S5).
	TraceExpressions
)

// Decision is the verdict on a request. A denial is a decision, never an error.
type Decision struct {
	Allow bool
	// Statements are the allow statements evaluated, in order, when the request asked for a
	// trace: for the log, the console and :test.
	Statements []StatementResult
}

// ErrOverloaded is what Evaluate returns when the memory of the evaluations running in the
// process has reached the Inflight cap. It says nothing about the rules: the service answers
// UNAVAILABLE, which the SDKs retry with backoff, and never allows.
var ErrOverloaded = errors.New("rules: too many evaluations in progress; try again")
