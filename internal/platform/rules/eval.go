package rules

import (
	"context"
	"errors"
	"fmt"
)

// The interpreter (ADR-021 §2.3). An error is a value: it travels up the tree until && or ||
// absorbs it, and a condition that ends in one does not grant. An operand of && or || that is
// not a boolean is an error like any other:
//
//	true || x, false && x    true and false; x is not evaluated
//	false || x, true && x    x when it is a boolean, else a type error
//	e || x                   e an error or not a boolean: true when x is true, else e
//	e && x                   e an error or not a boolean: false when x is false, else e
//	c ? a : b                c, then only the branch it chooses; c not a boolean is an error
//	!e, operators, calls     an error in an operand is the result
//
// Every operand of any other operator, every element of a list or a map literal and every
// argument of a call is evaluated even after one of them fails (the last first, as the emulator
// does), and the first error in source order is the result (Pyric, error-absorption-and-or). The
// arguments of a function of the file and its lets are bound to a value or an error, and fail
// only where the function reads them: ignoresArg(1/0) grants.
//
// An *abort ends the evaluation at once: a limit, or an outage (a cancelled context, a failing
// Documents, ErrOverloaded) that Evaluate returns instead of a decision.

// evaluator is the state of one evaluation: one operation of a request.
type evaluator struct {
	ctx  context.Context
	rs   *Ruleset
	req  *Request
	m    *meter
	vars []binding // the path variables of the block whose condition is evaluated

	count   int // expressions evaluated, across all of the evaluation's conditions
	lookups int // lookups made by this operation, cached ones excluded
	calls   int // functions of the file being called, one inside another
}

// frame is a call of a function of the file: its arguments and lets, each a value or an error.
type frame struct {
	params []slotValue
	lets   []slotValue
}

type slotValue struct {
	v   Value
	err error
}

// pollEvery is how many expressions pass between two looks at the context.
const pollEvery = 64

// spend counts n expressions, the expression limit being Firebase's for the whole evaluation
// ("Unable to evaluate the expression as the maximum of 1000 expressions to evaluate has been
// reached."). Past it the evaluation ends with a denial. The context is polled every pollEvery
// expressions.
func (ev *evaluator) spend(n int) error {
	before := ev.count
	ev.count += n
	if ev.count > ev.rs.lim.Expressions {
		return &abort{msg: fmt.Sprintf("Unable to evaluate the expression as the maximum of %d expressions to evaluate has been reached.", ev.rs.lim.Expressions)}
	}
	if before/pollEvery != ev.count/pollEvery {
		if err := ev.ctx.Err(); err != nil {
			return &abort{msg: "evaluation cancelled", err: err}
		}
	}
	return nil
}

// isAbort reports whether err ends the evaluation.
func isAbort(err error) bool {
	var a *abort
	return errors.As(err, &a)
}

// spanned gives an evaluation error that has no place yet the span of the expression it arose in.
// It copies the error, which the library may share.
func spanned(err error, at span) error {
	if e, ok := err.(*evalError); ok && e.at == (span{}) {
		c := *e
		c.at = at
		return &c
	}
	return err
}

// condition evaluates an allow statement's condition.
func (ev *evaluator) condition(e expr) (Value, error) {
	if e == nil {
		return Bool(true), nil // no condition: the statement grants
	}
	return ev.eval(e, nil)
}

// eval evaluates e in fr, nil outside a function. Each expression costs one when it starts;
// some cost more, as the emulator counts them (hidane, rules_expressions): the right side of &&
// and ||, a let, each literal segment of a path, the type of is, and the branches of ?:.
func (ev *evaluator) eval(e expr, fr *frame) (Value, error) {
	if e == nil {
		return Value{}, errorf("Null value error.")
	}
	if err := ev.spend(1); err != nil {
		return Value{}, err
	}
	var v Value
	var err error
	switch e := e.(type) {
	case *litExpr:
		return e.val, nil
	case *identExpr:
		v, err = ev.ident(e, fr)
	case *binaryExpr:
		if e.op == tokAnd || e.op == tokOr {
			v, err = ev.logical(e, fr)
		} else {
			v, err = ev.binary(e, fr)
		}
	case *unaryExpr:
		v, err = ev.unary(e, fr)
	case *condExpr:
		v, err = ev.cond(e, fr)
	case *callExpr:
		v, err = ev.call(e, fr)
	case *memberExpr:
		v, err = ev.member(e, fr)
	case *indexExpr:
		v, err = ev.index(e, fr)
	case *sliceExpr:
		v, err = ev.slice(e, fr)
	case *isExpr:
		v, err = ev.is(e, fr)
	case *listExpr:
		v, err = ev.list(e, fr)
	case *mapExpr:
		v, err = ev.mapLit(e, fr)
	case *pathExpr:
		v, err = ev.path(e, fr)
	default:
		err = errorf("Unsupported expression.")
	}
	if err != nil {
		// An error new here is a value the evaluation built, and may hold until it ends (the
		// operands and arguments keep theirs): its message is charged like any other (the
		// review's SR3-03), once, where it is placed.
		if ee, ok := err.(*evalError); ok && ee.at == (span{}) {
			if aerr := ev.m.alloc(errorBytes + int64(len(ee.msg))); aerr != nil {
				return Value{}, aerr
			}
		}
		return Value{}, spanned(err, e.pos())
	}
	return v, nil
}

// operands evaluates exprs, the last first, and returns their values, or the first error in
// source order. An abort ends it at once. Only that error is kept: each one met, the last
// first, comes before those met earlier.
func (ev *evaluator) operands(exprs []expr, fr *frame) ([]Value, error) {
	vals := make([]Value, len(exprs))
	var first error
	for i := len(exprs) - 1; i >= 0; i-- {
		v, err := ev.eval(exprs[i], fr)
		if isAbort(err) {
			return nil, err
		}
		if err != nil {
			first = err
		}
		vals[i] = v
	}
	if first != nil {
		return nil, first
	}
	return vals, nil
}

// ident reads a name the checker resolved.
func (ev *evaluator) ident(e *identExpr, fr *frame) (Value, error) {
	switch e.kind {
	case identParam:
		if fr != nil && e.slot >= 0 && e.slot < len(fr.params) {
			return fr.params[e.slot].v, fr.params[e.slot].err
		}
	case identLet:
		if fr != nil && e.slot >= 0 && e.slot < len(fr.lets) {
			return fr.lets[e.slot].v, fr.lets[e.slot].err
		}
	case identPathVar:
		if e.slot >= 0 && e.slot < len(ev.vars) {
			return ev.pathVar(e.slot)
		}
	case identGlobal:
		if v, ok := ev.req.Variables[e.name]; ok {
			return v, nil
		}
	}
	return Value{}, errorf("Null value error.")
}

// pathVar reads a path variable: a {x=**} builds its Path the first time it is read, charged to
// the meter.
func (ev *evaluator) pathVar(slot int) (Value, error) {
	b := &ev.vars[slot]
	switch {
	case !b.ok:
		return Value{}, &evalError{} // took an unknown segment: no value, no message
	case b.lazy:
		v, err := globPath(ev.m, b.run)
		if err != nil {
			return Value{}, err
		}
		*b = binding{v: v, ok: true}
	}
	return b.v, nil
}

// logical evaluates && and ||, which absorb an error when the other side decides.
func (ev *evaluator) logical(e *binaryExpr, fr *frame) (Value, error) {
	and := e.op == tokAnd // the value of the left side that leaves the result to the right
	l, lerr := ev.eval(e.x, fr)
	if isAbort(lerr) {
		return Value{}, lerr
	}
	if lerr == nil {
		b, ok := l.AsBool()
		if ok && b != and {
			return Bool(b), nil
		}
		if ok {
			if err := ev.spend(1); err != nil {
				return Value{}, err
			}
			r, rerr := ev.eval(e.y, fr)
			if rerr != nil {
				return Value{}, rerr
			}
			if rb, ok := r.AsBool(); ok {
				return Bool(rb), nil
			}
			return Value{}, spanned(notBool(r), e.y.pos())
		}
		lerr = spanned(notBool(l), e.x.pos())
	}
	if err := ev.spend(1); err != nil {
		return Value{}, err
	}
	r, rerr := ev.eval(e.y, fr)
	if isAbort(rerr) {
		return Value{}, rerr
	}
	if rerr == nil {
		if rb, ok := r.AsBool(); ok && rb != and {
			return Bool(rb), nil
		}
	}
	return Value{}, lerr
}

// notBool is the error of a value where a boolean is required.
func notBool(v Value) error { return errorf(msgTypeError, v.Kind(), KindBool) }

// cond evaluates c ? a : b: the condition, then only the branch it chooses, so that only that
// branch's lookups are made.
func (ev *evaluator) cond(e *condExpr, fr *frame) (Value, error) {
	c, err := ev.eval(e.test, fr)
	if isAbort(err) {
		return Value{}, err
	}
	if serr := ev.spend(1); serr != nil {
		return Value{}, serr
	}
	if err != nil {
		return Value{}, err
	}
	b, ok := c.AsBool()
	if !ok {
		return Value{}, spanned(notBool(c), e.test.pos())
	}
	if b {
		return ev.eval(e.yes, fr)
	}
	if err := ev.spend(1); err != nil {
		return Value{}, err
	}
	v, err := ev.eval(e.no, fr)
	if isAbort(err) {
		return Value{}, err
	}
	if serr := ev.spend(1); serr != nil {
		return Value{}, serr
	}
	return v, err
}

// unary evaluates ! and -. The interpreter negates a boolean itself; anything else is the
// library's, which refuses ! on what is not a boolean.
func (ev *evaluator) unary(e *unaryExpr, fr *frame) (Value, error) {
	x, err := ev.eval(e.x, fr)
	if err != nil {
		return Value{}, err
	}
	if e.op == tokNot {
		if b, ok := x.AsBool(); ok {
			return Bool(!b), nil
		}
	}
	return unary(ev.m, e.op, x)
}

// binary evaluates every binary operator but && and ||: both operands, then the library. The
// error of x in y is the collection's first (it is y's method).
func (ev *evaluator) binary(e *binaryExpr, fr *frame) (Value, error) {
	y, yerr := ev.eval(e.y, fr)
	if isAbort(yerr) {
		return Value{}, yerr
	}
	x, xerr := ev.eval(e.x, fr)
	if isAbort(xerr) {
		return Value{}, xerr
	}
	if e.op == tokIn && yerr != nil {
		return Value{}, yerr
	}
	if xerr != nil {
		return Value{}, xerr
	}
	if yerr != nil {
		return Value{}, yerr
	}
	return binary(ev.m, e.op, x, y)
}

// call evaluates a call the checker resolved.
func (ev *evaluator) call(e *callExpr, fr *frame) (Value, error) {
	switch e.kind {
	case callsUser:
		return ev.callUser(e, fr)
	case callsLookup:
		return ev.lookup(e, fr)
	case callsFunction:
		args, err := ev.operands(e.args, fr)
		if err != nil {
			return Value{}, err
		}
		return callFunction(ev.m, e.name, args)
	case callsMethod:
		mem, ok := e.fn.(*memberExpr)
		if !ok {
			break
		}
		// The arguments are evaluated before the receiver; the receiver's error comes first.
		args, aerr := ev.operands(e.args, fr)
		if isAbort(aerr) {
			return Value{}, aerr
		}
		recv, err := ev.eval(mem.x, fr)
		if err != nil {
			return Value{}, err
		}
		if aerr != nil {
			return Value{}, aerr
		}
		return callMethod(ev.m, recv, e.name, args)
	}
	// A function that does not exist fails before its arguments are evaluated.
	return Value{}, errorf(msgFunctionNotFound, clip(e.name))
}

// callUser calls a function of the file. Its arguments (the last first) and then its lets in
// order are evaluated before its result, each bound to its value or its error, which fails only
// where the function reads it (Pyric: ignoresArg(1/0) grants; hidane: the emulator evaluates
// them all first).
func (ev *evaluator) callUser(e *callExpr, fr *frame) (Value, error) {
	f := e.user
	if f == nil {
		return Value{}, errorf(msgFunctionNotFound, clip(e.name))
	}
	if len(e.args) != len(f.params) {
		return Value{}, errorf("Incorrect number of arguments. Received: %d. Expected: %d.", len(e.args), len(f.params))
	}
	// The checker bounds the depth of calls on the call graph; this only keeps the stack
	// bounded should a tree reach here unchecked.
	if ev.calls > ev.rs.lim.CallDepth {
		return Value{}, &abort{msg: fmt.Sprintf(msgCallDepth, ev.rs.lim.CallDepth, f.name.text)}
	}
	nf := &frame{params: make([]slotValue, len(e.args)), lets: make([]slotValue, len(f.lets))}
	for i := len(e.args) - 1; i >= 0; i-- {
		v, err := ev.eval(e.args[i], fr)
		if isAbort(err) {
			return Value{}, err
		}
		nf.params[i] = slotValue{v, err}
	}
	ev.calls++
	defer func() { ev.calls-- }()
	for i, l := range f.lets {
		if l == nil {
			nf.lets[i] = slotValue{err: errorf("Null value error.")}
			continue
		}
		v, err := ev.eval(l.value, nf)
		if isAbort(err) {
			return Value{}, err
		}
		nf.lets[i] = slotValue{v, err}
		if err := ev.spend(1); err != nil {
			return Value{}, err
		}
	}
	return ev.eval(f.ret, nf)
}

// member evaluates x.name.
func (ev *evaluator) member(e *memberExpr, fr *frame) (Value, error) {
	x, err := ev.eval(e.x, fr)
	if err != nil {
		return Value{}, err
	}
	return selectField(ev.m, x, e.name)
}

// index evaluates x[i]: the index first, then x, whose error comes first.
func (ev *evaluator) index(e *indexExpr, fr *frame) (Value, error) {
	i, ierr := ev.eval(e.i, fr)
	if isAbort(ierr) {
		return Value{}, ierr
	}
	x, err := ev.eval(e.x, fr)
	if err != nil {
		return Value{}, err
	}
	if ierr != nil {
		return Value{}, ierr
	}
	return selectIndex(ev.m, x, i)
}

// slice evaluates x[lo:hi]: the bounds first, the last first, then x, whose error comes first.
func (ev *evaluator) slice(e *sliceExpr, fr *frame) (Value, error) {
	var bounds [2]*Value
	var berr error
	for k, b := range [2]expr{e.hi, e.lo} {
		if b == nil {
			continue
		}
		v, err := ev.eval(b, fr)
		if isAbort(err) {
			return Value{}, err
		}
		if err != nil {
			berr = err // the lower bound's, evaluated last, comes first
		}
		bounds[1-k] = &v
	}
	x, err := ev.eval(e.x, fr)
	if err != nil {
		return Value{}, err
	}
	if berr != nil {
		return Value{}, berr
	}
	return selectSlice(ev.m, x, bounds[0], bounds[1])
}

// is evaluates x is typ. The type counts as an expression.
func (ev *evaluator) is(e *isExpr, fr *frame) (Value, error) {
	if err := ev.spend(1); err != nil {
		return Value{}, err
	}
	x, err := ev.eval(e.x, fr)
	if err != nil {
		return Value{}, err
	}
	return typeIs(x, e.typ)
}

// list evaluates a list literal, which the library builds and charges.
func (ev *evaluator) list(e *listExpr, fr *frame) (Value, error) {
	vals, err := ev.operands(e.elems, fr)
	if err != nil {
		return Value{}, err
	}
	return buildList(ev.m, vals)
}

// mapLit evaluates a map literal, which the library builds and charges: its keys must be
// strings, and a repeated key keeps the place of its first and the value of its last, so that
// keys() and values() give the written order.
func (ev *evaluator) mapLit(e *mapExpr, fr *frame) (Value, error) {
	if len(e.keys) != len(e.vals) {
		return Value{}, errorf("Unsupported expression.")
	}
	exprs := make([]expr, 0, 2*len(e.keys))
	for i := range e.keys {
		exprs = append(exprs, e.keys[i], e.vals[i])
	}
	vals, err := ev.operands(exprs, fr)
	if err != nil {
		return Value{}, err
	}
	keys := make([]Value, 0, len(e.keys))
	elems := make([]Value, 0, len(e.keys))
	for i := 0; i < len(vals); i += 2 {
		if _, ok := vals[i].AsString(); !ok {
			return Value{}, spanned(errorf(msgTypeError, vals[i].Kind(), KindString), e.keys[i/2].pos())
		}
		keys, elems = append(keys, vals[i]), append(elems, vals[i+1])
	}
	return buildMap(ev.m, keys, elems)
}

// path evaluates a path literal: its $(…) parts the last first, each literal segment counting
// as an expression, then the library builds the path.
func (ev *evaluator) path(e *pathExpr, fr *frame) (Value, error) {
	parts := make([]pathPart, len(e.segs))
	errs := make([]error, len(e.segs))
	for i := len(e.segs) - 1; i >= 0; i-- {
		s := e.segs[i]
		if s.x == nil {
			if err := ev.spend(1); err != nil {
				return Value{}, err
			}
			parts[i] = pathPart{text: s.text}
			continue
		}
		v, err := ev.eval(s.x, fr)
		if isAbort(err) {
			return Value{}, err
		}
		parts[i], errs[i] = pathPart{value: v, isVal: true}, err
	}
	for _, err := range errs {
		if err != nil {
			return Value{}, err
		}
	}
	return buildPath(ev.m, parts)
}
