package rules

import "fmt"

// The interpreter knows two kinds of error, and the library returns nothing else (ADR-021
// §2.3, §4):
//
//   - *evalError is a value: it travels up the tree until && or || absorbs it, and a condition
//     that ends in one does not grant.
//   - *abort ends the whole evaluation with a denial: an Aruma limit, or a Firebase limit for
//     as long as production is not shown to absorb it. When err is set, the cause is an outage
//     rather than the rules (ErrOverloaded, a failing Documents, a cancelled context), and
//     Evaluate returns err instead of a decision.

// evalError is an evaluation error. msg may be empty: an error on a reserved value (Constraint,
// Unknown, an unknown segment) carries no message.
type evalError struct {
	msg string
	at  span // where it arose; zero until the interpreter places it
}

func (e *evalError) Error() string { return e.msg }

// errorf returns an evaluation error with a formatted message and no position yet.
func errorf(format string, args ...any) *evalError {
	return &evalError{msg: fmt.Sprintf(format, args...)}
}

// abort ends an evaluation.
type abort struct {
	msg string
	err error // the outage behind the abort; nil when a limit of the rules was exceeded
}

func (a *abort) Error() string {
	if a.err != nil {
		return a.msg + ": " + a.err.Error()
	}
	return a.msg
}

func (a *abort) Unwrap() error { return a.err }
