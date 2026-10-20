package rules

import "regexp"

// expr is a node of a parsed rule expression. pos is the byte offset where the node starts,
// which load-time errors report.
type expr interface{ at() int }

type (
	// literal is null, a boolean, a number or a string.
	literal struct {
		pos int
		val any // nil, bool, float64 or string
	}
	// regexLit is /pattern/flags, valid only as the argument of matches().
	regexLit struct {
		pos int
		src string
		re  *regexp.Regexp
	}
	// arrayLit is [a, b], valid only as the argument of hasChildren().
	arrayLit struct {
		pos   int
		elems []expr
	}
	// ident is a variable: auth, now, root, data, newData, query or a $wildcard.
	ident struct {
		pos  int
		name string
	}
	// member is obj.name, or obj[key] when key is set.
	member struct {
		pos  int
		obj  expr
		name string // the property, or the key when it is a string literal
		key  expr   // a computed key that is not a string literal; nil otherwise
		// set by the checker:
		method method // the method this member names, when it is called
		recv   typ    // the static type of obj
	}
	call struct {
		pos    int
		callee expr // a *member naming a method, once the checker accepts it
		args   []expr
	}
	unary struct {
		pos int
		op  string // "!" or "-"
		x   expr
	}
	binary struct {
		pos  int
		op   string // "+", "-", "*", "/", "%", "==", "!=", "<", "<=", ">", ">=", "&&", "||"
		l, r expr
	}
	cond struct {
		pos           int
		test, yes, no expr
	}
)

func (e *literal) at() int  { return e.pos }
func (e *regexLit) at() int { return e.pos }
func (e *arrayLit) at() int { return e.pos }
func (e *ident) at() int    { return e.pos }
func (e *member) at() int   { return e.pos }
func (e *call) at() int     { return e.pos }
func (e *unary) at() int    { return e.pos }
func (e *binary) at() int   { return e.pos }
func (e *cond) at() int     { return e.pos }

// method is a snapshot or string method, resolved when the rule is loaded.
type method uint8

const (
	noMethod method = iota
	// RuleDataSnapshot
	mVal
	mChild
	mParent
	mHasChild
	mHasChildren
	mExists
	mGetPriority
	mIsNumber
	mIsString
	mIsBoolean
	// String
	mContains
	mBeginsWith
	mEndsWith
	mReplace
	mToLowerCase
	mToUpperCase
	mMatches
)

var snapshotMethods = map[string]method{
	"val": mVal, "child": mChild, "parent": mParent, "hasChild": mHasChild,
	"hasChildren": mHasChildren, "exists": mExists, "getPriority": mGetPriority,
	"isNumber": mIsNumber, "isString": mIsString, "isBoolean": mIsBoolean,
}

var stringMethods = map[string]method{
	"contains": mContains, "beginsWith": mBeginsWith, "endsWith": mEndsWith, "replace": mReplace,
	"toLowerCase": mToLowerCase, "toUpperCase": mToUpperCase, "matches": mMatches,
}

var methodNames = func() map[method]string {
	m := map[method]string{}
	for k, v := range snapshotMethods {
		m[v] = k
	}
	for k, v := range stringMethods {
		m[v] = k
	}
	return m
}()
