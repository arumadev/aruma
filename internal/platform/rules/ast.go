package rules

// span is the byte offsets of a node in its file: start inclusive, end exclusive. Issues and
// traces convert it to a Position (issues.go) only when they are reported.
type span struct{ start, end int }

func (s span) pos() span { return s }

// token is an operator or a punctuation mark. The parser, the interpreter and the library share
// it: the operators of the tree are tokens, and binary and unary receive them. Literals,
// identifiers and path segments are lexical classes of the lexer's own (lexer.go); keywords
// (service, match, allow, function, let, return, if, true, false, null, rules_version) are
// identifiers to the lexer, and the parser tells them apart by their text.
type token uint8

const (
	tokIllegal token = iota

	// Punctuation.
	tokLParen    // (
	tokRParen    // )
	tokLBrack    // [
	tokRBrack    // ]
	tokLBrace    // {
	tokRBrace    // }
	tokComma     // ,
	tokDot       // .
	tokColon     // :
	tokSemicolon // ;
	tokQuestion  // ?
	tokAssign    // =

	// Operators.
	tokNot     // !
	tokPlus    // +
	tokMinus   // -
	tokStar    // *
	tokSlash   // /
	tokPercent // %
	tokLT      // <
	tokLE      // <=
	tokGT      // >
	tokGE      // >=
	tokEQ      // ==
	tokNE      // !=
	tokAnd     // &&
	tokOr      // ||
	tokIn      // in
	tokIs      // is

	// Read as tokens so that they are refused by name, not as two signs (hidane).
	tokIncrement // ++
	tokDecrement // --
)

var tokenText = [...]string{
	tokIllegal:   "ILLEGAL",
	tokLParen:    "(",
	tokRParen:    ")",
	tokLBrack:    "[",
	tokRBrack:    "]",
	tokLBrace:    "{",
	tokRBrace:    "}",
	tokComma:     ",",
	tokDot:       ".",
	tokColon:     ":",
	tokSemicolon: ";",
	tokQuestion:  "?",
	tokAssign:    "=",
	tokNot:       "!",
	tokPlus:      "+",
	tokMinus:     "-",
	tokStar:      "*",
	tokSlash:     "/",
	tokPercent:   "%",
	tokLT:        "<",
	tokLE:        "<=",
	tokGT:        ">",
	tokGE:        ">=",
	tokEQ:        "==",
	tokNE:        "!=",
	tokAnd:       "&&",
	tokOr:        "||",
	tokIn:        "in",
	tokIs:        "is",
	tokIncrement: "++",
	tokDecrement: "--",
}

// String returns the token as it is written.
func (t token) String() string {
	if int(t) < len(tokenText) {
		return tokenText[t]
	}
	return "ILLEGAL"
}

// name is a name as written, with where it is: a function, a parameter, a let, a method in an
// allow statement.
type name struct {
	span
	text string
}

// file is a parsed rules file.
type file struct {
	span
	name string // the file's name, for issues
	src  string // the file's text, for positions

	// version is rules_version as written ("1", "2", or anything else, which the checker
	// refuses); empty when the file has none, which means 1 and is warned about.
	version   string
	versionAt span

	// service is the service's name as written, e.g. "cloud.firestore".
	service   string
	serviceAt span

	// functions are declared outside the service block, if the grammar allows it (S1-A).
	functions []*function
	// body is the content of the service block.
	body block
}

// block is the content of a service or match block, in source order within each kind. The
// order of match blocks is the order of evaluation (ADR-021 §5).
type block struct {
	functions []*function
	matches   []*match
	allows    []*allow
}

// match is a match block.
type match struct {
	span
	path []pattern // the segments of its path, after the parent's
	body block
}

// patternKind tells the kinds of match path segments apart.
type patternKind uint8

const (
	patternLiteral  patternKind = iota // images
	patternVariable                    // {x}: one segment, a string
	patternRest                        // {x=**}: a run of segments, a path
)

// pattern is one segment of a match path.
type pattern struct {
	span
	kind patternKind
	text string // the literal, or the variable's name
}

// allow is an allow statement.
type allow struct {
	span
	methods []name // as written: read, write, get, list, create, update, delete
	cond    expr   // nil when the statement has no condition, which grants
}

// function is a function declaration.
type function struct {
	span
	name   name
	params []name
	lets   []*let
	ret    expr // the returned expression
}

// let is a let binding in a function.
type let struct {
	span
	name  name
	value expr
}

// expr is an expression node.
type expr interface {
	pos() span
	exprNode()
}

// identKind is what an identifier refers to, as the checker resolves it.
type identKind uint8

const (
	identUnresolved identKind = iota // not checked, or unknown: an error when evaluated
	identGlobal                      // a variable the request supplies (request, resource): by name
	identPathVar                     // a variable of an enclosing match block: slot
	identParam                       // a parameter of the enclosing function: slot
	identLet                         // a let of the enclosing function: slot
	identNamespace                   // math, hashing, firestore…: valid only as a callee's receiver
)

// callKind is what a call reaches, as the checker resolves it.
type callKind uint8

const (
	callsUnresolved callKind = iota // not checked, or an unknown function: an error when evaluated
	callsUser                       // a function the file declares: user
	callsFunction                   // a global or namespaced library function: name, through callFunction
	callsMethod                     // a method of the receiver fn.(*memberExpr).x: name, through callMethod
	callsLookup                     // get, exists, getAfter, existsAfter, firestore.get, firestore.exists: name
)

type (
	// litExpr is a literal: null, true, false, an int, a float, a string or bytes.
	litExpr struct {
		span
		val Value
	}
	// identExpr is a name in an expression.
	identExpr struct {
		span
		name string
		// set by the checker:
		kind identKind
		slot int
	}
	// memberExpr is x.name.
	memberExpr struct {
		span
		x      expr
		name   string
		nameAt span
	}
	// indexExpr is x[i].
	indexExpr struct {
		span
		x, i expr
	}
	// sliceExpr is x[lo:hi]; a bound left out is nil.
	sliceExpr struct {
		span
		x, lo, hi expr
	}
	// callExpr is fn(args): a function when fn is an identExpr, a method or a namespaced
	// function when fn is a memberExpr.
	callExpr struct {
		span
		fn   expr
		args []expr
		// set by the checker:
		kind callKind
		user *function
		name string // the library name: "size", "math.abs", "firestore.get"
	}
	// unaryExpr is op x, op being tokNot or tokMinus.
	unaryExpr struct {
		span
		op token
		x  expr
	}
	// binaryExpr is x op y, for every binary operator but is: arithmetic, comparison, in, &&
	// and ||.
	binaryExpr struct {
		span
		op   token
		x, y expr
	}
	// isExpr is x is typ.
	isExpr struct {
		span
		x     expr
		typ   string // as written: string, int, number, map…
		typAt span
	}
	// condExpr is test ? yes : no.
	condExpr struct {
		span
		test, yes, no expr
	}
	// listExpr is [a, b].
	listExpr struct {
		span
		elems []expr
	}
	// mapExpr is {k: v, …}.
	mapExpr struct {
		span
		keys, vals []expr
	}
	// pathExpr is a path literal, /databases/$(database)/documents/users/$(uid).
	pathExpr struct {
		span
		segs []pathSeg
	}
)

// pathSeg is one segment of a path literal: literal text as written, still URL-encoded, or a
// $(x), whose value is one segment when it is a string and all of its segments when it is a
// path.
type pathSeg struct {
	span
	text string
	x    expr // the $(…) expression; nil for literal text
}

func (*litExpr) exprNode()    {}
func (*identExpr) exprNode()  {}
func (*memberExpr) exprNode() {}
func (*indexExpr) exprNode()  {}
func (*sliceExpr) exprNode()  {}
func (*callExpr) exprNode()   {}
func (*unaryExpr) exprNode()  {}
func (*binaryExpr) exprNode() {}
func (*isExpr) exprNode()     {}
func (*condExpr) exprNode()   {}
func (*listExpr) exprNode()   {}
func (*mapExpr) exprNode()    {}
func (*pathExpr) exprNode()   {}
