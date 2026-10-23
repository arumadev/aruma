package rules

// Outcome is what an allow statement's condition came to.
type Outcome uint8

const (
	OutcomeFalse Outcome = iota // false: the statement does not grant
	OutcomeTrue                 // true, or no condition: the statement grants
	OutcomeError                // an error or a value that is not a boolean: it does not grant
)

func (o Outcome) String() string {
	switch o {
	case OutcomeTrue:
		return "true"
	case OutcomeError:
		return "error"
	}
	return "false"
}

// StatementResult is one allow statement evaluated for a request.
type StatementResult struct {
	Position Position // where the statement is in the file
	Methods  []string // the methods it names, as written: read, write, get, list…
	Outcome  Outcome
	Message  string // the error's message when Outcome is OutcomeError; it may be empty
}
