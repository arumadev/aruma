package rules

import (
	"errors"
	"math"
	"regexp"
	"strconv"
)

// A rule that fails while it runs counts as false: "parent() will fail, causing the current
// rule expression to be skipped (as a failure)" (reference). A failure does not stop the rest
// of the check: a .write that fails at one level can still be granted deeper (emulator 4.11.2).

// evalError is a rule failing at run time.
type evalError struct{ msg string }

func (e *evalError) Error() string { return e.msg }

func fail(msg string) error { return &evalError{msg: msg} }

// Values at run time are nil, bool, float64, string, map[string]any and []any (from auth),
// branch, snap, *Query and *regexp.Regexp.

// branch is what val() returns for a node with children: "a special sentinel value"
// (reference) that equals nothing, itself included, and supports no operator.
type branch struct{}

// snap is a RuleDataSnapshot: a path into the stored data or into newData.
type snap struct {
	fresh bool
	path  []string
	// voidAt is the index of the first key in path that cannot exist, such as "a.b" from
	// child('a.b'), or -1. Such a snapshot is empty, as in Firebase, rather than an error.
	voidAt int
}

// evaluator runs one rule at one location.
type evaluator struct {
	w     *world
	auth  any // nil or map[string]any
	now   float64
	query *Query
	vars  map[string]string
	path  []string
}

// run evaluates a rule and reports whether it grants.
func (ev *evaluator) run(r *rule) bool {
	if r.expr == nil {
		return false
	}
	v, err := ev.eval(r.expr)
	return err == nil && v == true
}

func (ev *evaluator) eval(e expr) (any, error) {
	switch e := e.(type) {
	case *literal:
		return e.val, nil
	case *regexLit:
		return e.re, nil
	case *ident:
		switch e.name {
		case "auth":
			if ev.auth == nil {
				return nil, nil
			}
			return ev.auth, nil
		case "now":
			return ev.now, nil
		case "root":
			return snap{voidAt: -1}, nil
		case "data":
			return snap{path: ev.path, voidAt: -1}, nil
		case "newData":
			return snap{fresh: true, path: ev.path, voidAt: -1}, nil
		case "query":
			return ev.query, nil
		}
		return ev.vars[e.name], nil
	case *member:
		return ev.member(e)
	case *call:
		return ev.call(e)
	case *unary:
		x, err := ev.eval(e.x)
		if err != nil {
			return nil, err
		}
		if e.op == "!" {
			b, ok := x.(bool)
			if !ok {
				return nil, fail("! needs a boolean")
			}
			return !b, nil
		}
		n, ok := x.(float64)
		if !ok {
			return nil, fail("- needs a number")
		}
		return -n, nil
	case *binary:
		return ev.binary(e)
	case *cond:
		t, err := ev.eval(e.test)
		if err != nil {
			return nil, err
		}
		b, ok := t.(bool)
		if !ok {
			return nil, fail("the condition of ? is not a boolean")
		}
		if b {
			return ev.eval(e.yes)
		}
		return ev.eval(e.no)
	}
	return nil, fail("unexpected expression")
}

func (ev *evaluator) binary(e *binary) (any, error) {
	l, err := ev.eval(e.l)
	if err != nil {
		return nil, err
	}
	if e.op == "&&" || e.op == "||" {
		lb, ok := l.(bool)
		if !ok {
			return nil, fail(e.op + " needs booleans")
		}
		if lb == (e.op == "||") {
			return lb, nil
		}
		r, err := ev.eval(e.r)
		if err != nil {
			return nil, err
		}
		rb, ok := r.(bool)
		if !ok {
			return nil, fail(e.op + " needs booleans")
		}
		return rb, nil
	}
	r, err := ev.eval(e.r)
	if err != nil {
		return nil, err
	}
	switch e.op {
	case "==", "!=":
		eq, err := equal(l, r)
		if err != nil {
			return nil, err
		}
		return eq == (e.op == "=="), nil
	case "<", "<=", ">", ">=":
		return compare(e.op, l, r)
	case "+":
		switch x := l.(type) {
		case float64:
			switch y := r.(type) {
			case float64:
				return x + y, nil
			case string:
				return javaNumber(x) + y, nil
			}
		case string:
			switch y := r.(type) {
			case float64:
				return x + javaNumber(y), nil
			case string:
				return x + y, nil
			}
		}
		return nil, fail("+ needs numbers or strings")
	}
	x, ok1 := l.(float64)
	y, ok2 := r.(float64)
	if !ok1 || !ok2 {
		return nil, fail(e.op + " needs numbers")
	}
	switch e.op {
	case "-":
		return x - y, nil
	case "*":
		return x * y, nil
	case "/":
		if y == 0 { // 1/0 is NaN in rules, not Infinity (emulator 4.11.2)
			return math.NaN(), nil
		}
		return x / y, nil
	default:
		return math.Mod(x, y), nil
	}
}

// equal is == in rules: the same type and value. Objects, arrays and the sentinel of a node
// with children equal nothing (emulator 4.11.2: auth.token == auth.token is false).
func equal(l, r any) (bool, error) {
	for _, v := range []any{l, r} {
		switch v.(type) {
		case nil, bool, float64, string, map[string]any, []any, branch:
		default:
			return false, fail("== needs primitive values")
		}
	}
	switch x := l.(type) {
	case nil:
		return r == nil, nil
	case bool:
		y, ok := r.(bool)
		return ok && x == y, nil
	case float64:
		y, ok := r.(float64)
		return ok && x == y, nil
	case string:
		y, ok := r.(string)
		return ok && x == y, nil
	}
	return false, nil
}

// compare orders two numbers or two strings; anything else fails, null included.
func compare(op string, l, r any) (any, error) {
	var c int
	switch x := l.(type) {
	case float64:
		y, ok := r.(float64)
		if !ok {
			return nil, fail(op + " needs two numbers or two strings")
		}
		if math.IsNaN(x) || math.IsNaN(y) {
			return false, nil
		}
		switch {
		case x < y:
			c = -1
		case x > y:
			c = 1
		}
	case string:
		y, ok := r.(string)
		if !ok {
			return nil, fail(op + " needs two numbers or two strings")
		}
		c = compareUTF16(x, y)
	default:
		return nil, fail(op + " needs two numbers or two strings")
	}
	switch op {
	case "<":
		return c < 0, nil
	case "<=":
		return c <= 0, nil
	case ">":
		return c > 0, nil
	}
	return c >= 0, nil
}

func (ev *evaluator) member(e *member) (any, error) {
	obj, err := ev.eval(e.obj)
	if err != nil {
		return nil, err
	}
	if q, ok := obj.(*Query); ok {
		return q.field(e.name), nil
	}
	key := e.name
	if e.key != nil {
		k, err := ev.eval(e.key)
		if err != nil {
			return nil, err
		}
		switch k := k.(type) {
		case string:
			key = k
		case float64:
			key = javaNumber(k)
		default:
			return nil, fail("a property name must be a string or a number")
		}
	}
	if e.recv == tStr || e.recv == tPrim {
		// .length of a value that is not a string fails, null included, where the same
		// property of something under auth is null (emulator 4.11.2).
		if s, ok := obj.(string); ok && key == "length" {
			return float64(utf16Len(s)), nil
		}
		return nil, fail("length needs a string")
	}
	switch o := obj.(type) {
	case nil:
		return nil, nil // auth.uid is null when auth is (reference)
	case map[string]any:
		return o[key], nil
	case []any:
		if i, err := strconv.Atoi(key); err == nil && i >= 0 && i < len(o) && strconv.Itoa(i) == key {
			return o[i], nil
		}
		return nil, nil
	case string:
		if key == "length" {
			return float64(utf16Len(o)), nil
		}
	}
	return nil, fail("no property " + key)
}

func (ev *evaluator) call(e *call) (any, error) {
	m := e.callee.(*member)
	recv, err := ev.eval(m.obj)
	if err != nil {
		return nil, err
	}
	args := make([]any, len(e.args))
	for i, a := range e.args {
		if arr, ok := a.(*arrayLit); ok {
			vals := make([]any, len(arr.elems))
			for j, el := range arr.elems {
				if vals[j], err = ev.eval(el); err != nil {
					return nil, err
				}
			}
			args[i] = vals
			continue
		}
		if args[i], err = ev.eval(a); err != nil {
			return nil, err
		}
	}
	if s, ok := recv.(snap); ok {
		return ev.snapshotMethod(m.method, s, args)
	}
	str, ok := recv.(string)
	if !ok {
		return nil, fail(methodNames[m.method] + "() needs a string")
	}
	return stringMethod(m.method, str, args)
}

var errNotString = errors.New("not a string")

func stringArgs(args []any) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		s, ok := a.(string)
		if !ok {
			return nil, errNotString
		}
		out[i] = s
	}
	return out, nil
}

func stringMethod(m method, s string, args []any) (any, error) {
	if m == mMatches {
		re, ok := args[0].(*regexp.Regexp)
		if !ok {
			return nil, fail("matches() needs a regular expression")
		}
		return re.MatchString(s), nil
	}
	a, err := stringArgs(args)
	if err != nil {
		return nil, fail(methodNames[m] + "() needs string arguments")
	}
	switch m {
	case mContains:
		return containsString(s, a[0]), nil
	case mBeginsWith:
		return len(s) >= len(a[0]) && s[:len(a[0])] == a[0], nil
	case mEndsWith:
		return len(s) >= len(a[0]) && s[len(s)-len(a[0]):] == a[0], nil
	case mReplace:
		return replaceAll(s, a[0], a[1]), nil
	case mToLowerCase:
		return javaLower(s), nil
	case mToUpperCase:
		return javaUpper(s), nil
	}
	return nil, fail("unknown string method")
}
