package rules

import (
	"errors"
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
func Compile(svc Service, f File, lim Limits) (*Ruleset, Issues) {
	panic("S1-C: Compile")
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

	// The compiled match blocks and functions are check's (S1-C).
}

// Version returns the file's rules_version: 1 or 2.
func (rs *Ruleset) Version() int { return rs.version }

// Service returns the service the ruleset was compiled for.
func (rs *Ruleset) Service() Service { return rs.service }

// Weight estimates the bytes the ruleset holds, for tenancy's cache of compiled rulesets, which
// is bounded by weight rather than count (ADR-021 §4, §7): its text, the programs of its literal
// patterns, and the cap of its cache of patterns built during evaluation, which it may fill.
func (rs *Ruleset) Weight() int64 {
	w := int64(len(rs.src)) + rs.lim.RegexCacheBytes
	if rs.regexes != nil {
		w += rs.regexes.literalBytes()
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
