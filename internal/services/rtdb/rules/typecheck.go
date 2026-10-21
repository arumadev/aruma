package rules

import "fmt"

// Firebase checks the types of a rule when it is loaded and refuses, for example, a snapshot
// compared with null or a rule that is not a boolean. The checker does the same with the
// messages Firebase uses (emulator 4.11.2, 2026-10-20), and resolves every method call so that
// evaluation never meets an unknown name.

// typ is a static type.
type typ uint8

const (
	tBool typ = iota + 1
	tNum
	tStr
	tNull
	tPrim  // a string, number, boolean or null known only at run time: val(), query.startAt
	tAny   // anything, known only at run time: auth and what hangs from it
	tSnap  // a RuleDataSnapshot: root, data, newData
	tQuery // the query variable
	tRegex
	tArray
	tMethod // a member naming a method; valid only when called
)

func (t typ) fuzzy() bool { return t == tAny || t == tPrim }

// primitive reports a type whose values are null, booleans, numbers or strings.
func (t typ) primitive() bool {
	return t == tBool || t == tNum || t == tStr || t == tNull || t == tPrim
}

func (t typ) oneOf(ts ...typ) bool {
	if t.fuzzy() {
		return true
	}
	for _, x := range ts {
		if t == x {
			return true
		}
	}
	return false
}

// queryFields types the members of the query variable.
var queryFields = map[string]typ{
	"orderByKey": tBool, "orderByPriority": tBool, "orderByValue": tBool,
	"orderByChild": tPrim, "startAt": tPrim, "endAt": tPrim, "equalTo": tPrim,
	"limitToFirst": tPrim, "limitToLast": tPrim,
}

type checker struct {
	kind ruleKind
	vars map[string]bool
}

// check type-checks a parsed rule: it must be a boolean expression over the variables in scope.
func check(e expr, kind ruleKind, vars []string) *exprError {
	c := &checker{kind: kind, vars: map[string]bool{}}
	for _, v := range vars {
		c.vars[v] = true
	}
	t, err := c.typeOf(e)
	if err != nil {
		return err
	}
	if t != tBool {
		return errAt(e.at(), "Expression must evaluate to a boolean.")
	}
	return nil
}

func (c *checker) typeOf(e expr) (typ, *exprError) {
	switch e := e.(type) {
	case *literal:
		switch e.val.(type) {
		case nil:
			return tNull, nil
		case bool:
			return tBool, nil
		case float64:
			return tNum, nil
		default:
			return tStr, nil
		}
	case *regexLit:
		re, err := compileRuleRegex(e.src)
		if err != nil {
			return 0, errAt(e.pos, err.Error())
		}
		e.re = re
		return tRegex, nil
	case *arrayLit:
		return 0, errAt(e.pos, "Unexpected array literal.")
	case *ident:
		switch e.name {
		case "auth":
			return tAny, nil
		case "now":
			return tNum, nil
		case "root", "data":
			return tSnap, nil
		case "query":
			return tQuery, nil
		case "newData":
			if c.kind == kindRead {
				return 0, errAt(e.pos, "newData is invalid in .read expressions.")
			}
			return tSnap, nil
		}
		if c.vars[e.name] {
			return tStr, nil
		}
		return 0, errAt(e.pos, fmt.Sprintf("Unknown variable '%s'.", e.name))
	case *member:
		return c.member(e)
	case *call:
		return c.call(e)
	case *unary:
		t, err := c.typeOf(e.x)
		if err != nil {
			return 0, err
		}
		if e.op == "!" {
			if t != tBool {
				return 0, errAt(e.pos, "! only operates on booleans.")
			}
			return tBool, nil
		}
		if !t.oneOf(tNum) {
			return 0, errAt(e.pos, "- only operates on numbers.")
		}
		return tNum, nil
	case *binary:
		return c.binary(e)
	case *cond:
		t, err := c.typeOf(e.test)
		if err != nil {
			return 0, err
		}
		if t != tBool {
			return 0, errAt(e.pos, "condition of ? must be boolean.")
		}
		yes, err := c.typeOf(e.yes)
		if err != nil {
			return 0, err
		}
		no, err := c.typeOf(e.no)
		if err != nil {
			return 0, err
		}
		switch {
		case yes == no:
			return yes, nil
		case yes.primitive() && no.primitive():
			return tPrim, nil
		default:
			return tAny, nil
		}
	}
	return 0, errAt(e.at(), "Unexpected expression.")
}

func (c *checker) binary(e *binary) (typ, *exprError) {
	l, err := c.typeOf(e.l)
	if err != nil {
		return 0, err
	}
	r, err := c.typeOf(e.r)
	if err != nil {
		return 0, err
	}
	side := func(ok func(typ) bool, msg string) *exprError {
		if !ok(l) {
			return errAt(e.pos, fmt.Sprintf("Invalid %s expression: left operand %s", e.op, msg))
		}
		if !ok(r) {
			return errAt(e.pos, fmt.Sprintf("Invalid %s expression: right operand %s", e.op, msg))
		}
		return nil
	}
	switch e.op {
	case "&&", "||":
		if l != tBool {
			return 0, errAt(e.pos, fmt.Sprintf("Left operand of %s must be boolean.", e.op))
		}
		if r != tBool {
			return 0, errAt(e.pos, fmt.Sprintf("Right operand of %s must be boolean.", e.op))
		}
		return tBool, nil
	case "==", "!=":
		eq := func(t typ) bool { return t.oneOf(tNum, tBool, tStr, tNull) }
		return tBool, side(eq, "is not a number, boolean, string, null.")
	case "<", "<=", ">", ">=":
		ord := func(t typ) bool { return t.oneOf(tNum, tStr) }
		return tBool, side(ord, "must be a number or string.")
	case "+":
		if err := side(func(t typ) bool { return t.oneOf(tNum, tStr) }, "is not a number or string."); err != nil {
			return 0, err
		}
		switch {
		case l == tStr || r == tStr:
			return tStr, nil
		case l == tNum && r == tNum:
			return tNum, nil
		default:
			return tPrim, nil
		}
	default: // - * / %
		return tNum, side(func(t typ) bool { return t.oneOf(tNum) }, "is not a number.")
	}
}

func (c *checker) member(e *member) (typ, *exprError) {
	recv, err := c.typeOf(e.obj)
	if err != nil {
		return 0, err
	}
	e.recv = recv
	if e.key != nil {
		if _, err := c.typeOf(e.key); err != nil {
			return 0, err
		}
		if recv == tAny {
			return tAny, nil
		}
		return 0, errAt(e.pos, "Invalid property access.")
	}
	noSuch := errAt(e.pos, fmt.Sprintf("No such method/property '%s'.", e.name))
	switch recv {
	case tAny:
		return tAny, nil
	case tSnap:
		m, ok := snapshotMethods[e.name]
		if !ok {
			return 0, noSuch
		}
		if m == mGetPriority {
			return 0, errAt(e.pos, "getPriority() is not supported.")
		}
		e.method = m
		return tMethod, nil
	case tStr, tPrim:
		if e.name == "length" {
			return tNum, nil
		}
		m, ok := stringMethods[e.name]
		if !ok {
			return 0, noSuch
		}
		e.method = m
		return tMethod, nil
	case tQuery:
		t, ok := queryFields[e.name]
		if !ok {
			return 0, noSuch
		}
		return t, nil
	}
	return 0, noSuch
}

func (c *checker) call(e *call) (typ, *exprError) {
	notFunc := errAt(e.pos, "Type error: Function call on target that is not a function.")
	m, ok := e.callee.(*member)
	if !ok {
		if _, err := c.typeOf(e.callee); err != nil {
			return 0, err
		}
		return 0, notFunc
	}
	t, err := c.typeOf(m)
	if err != nil {
		return 0, err
	}
	switch {
	case t == tMethod:
	case m.recv == tAny && m.key == nil:
		// A method called on auth or what hangs from it can only be a string method; the
		// receiver is checked when the rule runs.
		mm, ok := stringMethods[m.name]
		if !ok {
			return 0, errAt(m.pos, fmt.Sprintf("No such method/property '%s'.", m.name))
		}
		m.method = mm
	default:
		return 0, notFunc
	}
	name := methodNames[m.method]
	args := e.args
	arity := func(n int, msg string) *exprError {
		if len(args) != n {
			return errAt(e.pos, fmt.Sprintf("%s() expects %s.", name, msg))
		}
		return nil
	}
	stringArg := func(a expr) *exprError {
		t, err := c.typeOf(a)
		if err != nil {
			return err
		}
		if !t.oneOf(tStr) {
			return errAt(a.at(), fmt.Sprintf("%s() expects a string argument.", name))
		}
		return nil
	}
	switch m.method {
	case mVal, mParent, mExists, mIsNumber, mIsString, mIsBoolean, mToLowerCase, mToUpperCase:
		if err := arity(0, "no arguments"); err != nil {
			return 0, err
		}
		switch m.method {
		case mVal:
			return tPrim, nil
		case mParent:
			return tSnap, nil
		case mToLowerCase, mToUpperCase:
			return tStr, nil
		}
		return tBool, nil
	case mChild, mHasChild, mContains, mBeginsWith, mEndsWith:
		if err := arity(1, "1 argument"); err != nil {
			return 0, err
		}
		if err := stringArg(args[0]); err != nil {
			return 0, err
		}
		if m.method == mChild {
			return tSnap, nil
		}
		return tBool, nil
	case mReplace:
		if err := arity(2, "two arguments"); err != nil {
			return 0, err
		}
		for _, a := range args {
			if err := stringArg(a); err != nil {
				return 0, err
			}
		}
		return tStr, nil
	case mMatches:
		if len(args) != 1 {
			return 0, errAt(e.pos, "matches() expects 1 argument.")
		}
		if _, ok := args[0].(*regexLit); !ok {
			return 0, errAt(args[0].at(), "matches() expects a regular expression literal argument.")
		}
		if _, err := c.typeOf(args[0]); err != nil {
			return 0, err
		}
		return tBool, nil
	case mHasChildren:
		if len(args) == 0 {
			return tBool, nil
		}
		if len(args) > 1 {
			return 0, errAt(args[1].at(), "hasChildren() expects only a single argument (containing an array of child names).")
		}
		arr, ok := args[0].(*arrayLit)
		if !ok {
			return 0, errAt(args[0].at(), "hasChildren() expects an array of child names.")
		}
		for _, el := range arr.elems {
			t, err := c.typeOf(el)
			if err != nil {
				return 0, err
			}
			if !t.oneOf(tStr) {
				return 0, errAt(el.at(), "hasChildren() expects an array of strings.")
			}
		}
		return tBool, nil
	}
	return 0, notFunc
}
