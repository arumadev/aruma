package rules

import (
	"fmt"
	"unicode/utf8"
)

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

// errorf returns an evaluation error with a formatted message and no position yet. A value
// the message names passes through clip.
func errorf(format string, args ...any) *evalError {
	return &evalError{msg: fmt.Sprintf(format, args...)}
}

// messageValueBytes is the most bytes of a value that an error message copies.
const messageValueBytes = 128

// clip returns a value as a message names it: whole up to messageValueBytes, or its first
// messageValueBytes, back to a character's start, and "...". A rule chooses how large its values
// are, up to value_bytes, and errors copied them whole: a hundred errors on a 2 MiB string held
// hundreds of MiB the meter never saw (the review's SR3-03). The values production's messages
// are compared on are short, and unchanged.
func clip(s string) string {
	if len(s) <= messageValueBytes {
		return s
	}
	n := messageValueBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "..."
}

// errorBytes estimates what an evaluation error holds besides its message, for the meter.
const errorBytes = 64

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
