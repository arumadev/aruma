package rules

import (
	"errors"
	"fmt"
	"strings"
)

// Severity is how grave an issue is. Only an error stops a file from compiling.
type Severity uint8

const (
	SeverityError Severity = iota + 1
	SeverityWarning
)

// String returns the severity as the Rules API writes it.
func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "ERROR"
	case SeverityWarning:
		return "WARNING"
	}
	return "SEVERITY_UNSPECIFIED"
}

// Position is where an issue lies in its file, as the Rules API's sourcePosition carries it.
// The tree holds byte offsets; they are converted when an issue is reported, in the unit the
// oracle shows (code points, as the emulator counts, until S0-A measures production).
type Position struct {
	Line, Column             int // 1-based
	CurrentOffset, EndOffset int // from the start of the file
}

// Issue is one finding, as the Rules API's issues[] carries it.
type Issue struct {
	File        string
	Position    *Position // nil for an issue about the whole file: the wire form has no position
	Severity    Severity
	Description string
}

// String formats the issue as firebase-tools prints it: "[E] 5:10 - description".
func (i Issue) String() string {
	sev := "W"
	if i.Severity == SeverityError {
		sev = "E"
	}
	if i.Position == nil {
		return fmt.Sprintf("[%s] %s", sev, i.Description)
	}
	return fmt.Sprintf("[%s] %d:%d - %s", sev, i.Position.Line, i.Position.Column, i.Description)
}

// Issues are a file's findings in source order.
type Issues []Issue

// Err returns nil when no issue is an error, and otherwise an error listing the errors.
func (is Issues) Err() error {
	var lines []string
	file := ""
	for _, i := range is {
		if i.Severity == SeverityError {
			lines = append(lines, i.String())
			file = i.File
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return errors.New("rules: compilation errors in " + file + ":\n" + strings.Join(lines, "\n"))
}

// The fixed texts of the messages the oracle shows, shared by the parser, the checker and the
// interpreter. Descriptions are copied from the oracle; the decision tests compare them.
const (
	msgTooComplex       = "Expression is too complex to evaluate safely."
	msgCallDepth        = "Maximum allowed call depth of %d is reached for [%s] call stack."
	msgLetVersion1      = `Let bindings are not allowed in rules_version="1"`
	msgNoMatch          = "No matching allow statements"
	msgTypeError        = "Type error. Received: [%s] Expected: [%s]."
	msgFunctionNotFound = "Function not found error: Name: [%s]."
)

// position converts a span of byte offsets in src to the Position an issue reports.
// S1-A replaces this stub.
func position(src string, at span) Position {
	panic("S1-A: position")
}
