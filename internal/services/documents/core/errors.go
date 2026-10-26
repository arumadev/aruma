package core

import (
	"fmt"
	"strconv"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

// Code is an error's canonical status, numbered as gRPC numbers it; each surface maps it to its
// own (REST's HTTP status and name).
type Code uint8

const (
	InvalidArgument    Code = 3
	NotFound           Code = 5
	AlreadyExists      Code = 6
	FailedPrecondition Code = 9
	Aborted            Code = 10
	Unavailable        Code = 14
)

// Error is a failure the client is told of: a code and production's message, byte for byte where
// production's was recorded (test/oracle/documents/corpus/f1-*.json), marked provisional where not.
type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string { return "documents: " + e.Message }

var (
	// ErrReadTimeTooOld is a read_time before the database's version retention (f1-get.json,
	// rest.read_time_2h).
	ErrReadTimeTooOld = &Error{FailedPrecondition, "The requested 'read_time' is too old."}
	// ErrReadTimeFuture is a read_time after now (f1-get.json, rest.read_time.future).
	ErrReadTimeFuture = &Error{InvalidArgument, "The requested 'read_time' cannot be in the future."}
	// ErrContention is a commit that waited too long for its locks (ADR-024 §2.3, §6.2).
	ErrContention = &Error{Aborted, "Too much contention on these documents. Please try again."}
	// ErrUnknownOutcome is a commit whose connection was lost after COMMIT was sent: it may have
	// applied, so it is never ABORTED, which clients retry blindly (ADR-024 §2.1). Provisional
	// text: production does not lose commits this way.
	ErrUnknownOutcome = &Error{Unavailable, "The commit's outcome is unknown: the connection to the database was lost. Read the documents again before retrying."}
	// ErrNotServing is every call while this process does not hold the single-instance lock
	// (ADR-024 §7). Our own text.
	ErrNotServing = &Error{Unavailable, "The Document Database is not served by this Aruma instance: another instance holds it on this Postgres database, or the connection to Postgres was lost. The Document Database runs as a single instance."}
)

// documentNotFound is GetDocument of a document that does not exist (f0-list-show-missing.json,
// rest.get_missing_document; f1-get.json, rest.missing).
func documentNotFound(name string) *Error {
	return &Error{NotFound, "Document \"" + name + "\" not found."}
}

// noDocumentToUpdate is a precondition exists: true on a document that does not exist, for an
// update and a delete alike (f1-update.json, f1-delete.json).
func noDocumentToUpdate(name string) *Error {
	return &Error{NotFound, "No document to update: " + name}
}

// alreadyExists is a precondition exists: false on a document that exists (f1-create.json).
func alreadyExists(name string) *Error {
	return &Error{AlreadyExists, "Document already exists: " + name}
}

// staleVersion is a precondition update_time that does not match: the versions are update times
// in microseconds since the epoch, 0 for a document that does not exist (f1-update.json,
// f1-delete.json).
func staleVersion(stored, required time.Time) *Error {
	return &Error{FailedPrecondition, "the stored version (" + strconv.FormatInt(micros(stored), 10) +
		") does not match the required base version (" + strconv.FormatInt(micros(required), 10) + ")"}
}

// documentTooLarge is a document over 1 MiB after its write (f0-errors.json, doc_size.*).
func documentTooLarge(name string, size int) *Error {
	return &Error{InvalidArgument, fmt.Sprintf("Document '%s' cannot be written because its size (%s bytes) exceeds the maximum allowed size of %s bytes.",
		name, grouped(size), grouped(value.MaxDocumentSize))}
}

// invalidDocument is a document that value.Validate refuses once its update mask applied.
func invalidDocument(err error) *Error { return &Error{InvalidArgument, err.Error()} }

func micros(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMicro()
}

// grouped formats n with a comma every three digits, as production's size messages do.
func grouped(n int) string {
	s := strconv.Itoa(n)
	out := make([]byte, 0, len(s)+len(s)/3)
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
}

// DatabaseNotFoundError is a database that does not exist. Production words it differently on
// each surface (f0-missing-database.json): the data API over REST, over gRPC (code 5, an empty
// message, on Listen too) and the Admin API.
type DatabaseNotFoundError struct {
	ProjectID  string
	DatabaseID string
}

func (e *DatabaseNotFoundError) Error() string {
	return "documents: database " + e.DatabaseID + " of project " + e.ProjectID + " does not exist"
}

// RESTMessage is the data API's text over REST: no full stop after the project id, and a space
// after the last one.
func (e *DatabaseNotFoundError) RESTMessage() string {
	return "The database " + e.DatabaseID + " does not exist for project " + e.ProjectID +
		" Please visit https://console.cloud.google.com/datastore/setup?project=" + e.ProjectID +
		" to add a Cloud Datastore or Cloud Firestore database. "
}

// Code is NotFound on every surface.
func (e *DatabaseNotFoundError) Code() Code { return NotFound }

// GRPCMessage is the data API's text over gRPC: none.
func (e *DatabaseNotFoundError) GRPCMessage() string { return "" }

// AdminMessage is the Admin API's text (databases.get).
func (e *DatabaseNotFoundError) AdminMessage() string {
	return "Project '" + e.ProjectID + "' or database '" + e.DatabaseID + "' does not exist."
}
