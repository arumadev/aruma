package wire

import (
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/index"
)

// The errors here carry production's texts, byte for byte, recorded under
// test/oracle/documents/corpus. A text marked provisional was not recorded: it is the closest
// form known and is replaced once the recorder captures production's (ADR-028 §7.6).

// Google's generic texts for errors that carry no message of their own.
const (
	msgInternal         = "Internal error encountered."
	msgCanceled         = "The operation was cancelled."
	msgDeadlineExceeded = "Deadline expired before operation could complete."
)

// InvalidArgument is INVALID_ARGUMENT with message: the code of every request production
// refuses for its content (f0-errors.json, values-*.json).
func InvalidArgument(message string) *Error { return NewError(CodeInvalidArgument, message) }

// Internal is INTERNAL with Google's generic text.
func Internal() *Error { return NewError(CodeInternal, msgInternal) }

// Unimplemented is UNIMPLEMENTED (HTTP 501) for a feature production supports and this server
// does not yet: "<feature> is not supported by this server" (ADR-028 §6), e.g. Unimplemented(
// "Query Explain").
func Unimplemented(feature string) *Error {
	return NewError(CodeUnimplemented, feature+" is not supported by this server")
}

// MissingDatabase is NOT_FOUND for a database that does not exist, as production answers the
// data plane (f0-missing-database.json): on REST the text below, which has no full stop after
// the project id and ends with a space; on gRPC, Listen and Write included, an empty message
// and no details.
func MissingDatabase(db Database) *Error {
	return NewError(CodeNotFound, "The database "+db.ID+" does not exist for project "+db.Project+
		" Please visit https://console.cloud.google.com/datastore/setup?project="+db.Project+
		" to add a Cloud Datastore or Cloud Firestore database. ").WithGRPC("")
}

// MissingDatabaseAdmin is NOT_FOUND for a database that does not exist on the Admin API's
// GET projects/{p}/databases/{d} (f0-missing-database.json, admin.getDatabase). gRPC carries
// the same text (provisional: only REST was recorded).
func MissingDatabaseAdmin(db Database) *Error {
	return NewError(CodeNotFound, "Project '"+db.Project+"' or database '"+db.ID+"' does not exist.")
}

// InvalidResourceName is INVALID_ARGUMENT for an Admin API or header name that does not parse
// (provisional text).
func InvalidResourceName(name string) *Error {
	return InvalidArgument(`Invalid resource name "` + name + `".`)
}

// The Admin API is an admin's (ADR-029 §2, ADR-025 §4.5): a request without a credential is
// UNAUTHENTICATED and an end user's PERMISSION_DENIED, in Google's front end's texts for a
// management API (provisional: f3-admin.json records an owner's answers only).
var (
	ErrAdminUnauthenticated = NewError(CodeUnauthenticated, "Request is missing required authentication credential. "+
		"Expected OAuth 2 access token, login cookie or other valid authentication credential. "+
		"See https://developers.google.com/identity/sign-in/web/devconsole-project.")
	ErrAdminPermissionDenied = NewError(CodePermissionDenied, "The caller does not have permission")
)

// DocumentNotFound is NOT_FOUND for a GetDocument of a document that does not exist
// (f0-list-show-missing.json: `Document "projects/…/m" not found.`).
func DocumentNotFound(name string) *Error {
	return NewError(CodeNotFound, `Document "`+name+`" not found.`)
}

// InvalidTransaction is INVALID_ARGUMENT for a transaction id production never issued, on
// commit, the reads and rollback alike (f0-errors.json, txn_unknown.*; ADR-025 §7.5).
func InvalidTransaction() *Error { return InvalidArgument("Invalid transaction.") }

// TransactionExpired is ABORTED (HTTP 409) for a transaction that is over: committed, rolled
// back, expired or lost (f0-errors.json, txn_reused.commit_second; ADR-025 §7.5).
func TransactionExpired() *Error {
	return NewError(CodeAborted, "The referenced transaction has expired or is no longer valid.")
}

// The two ABORTED texts of pessimistic contention (f0-contention.json; ADR-025 §7.3): a waiter
// that timed out, and a younger transaction wounded by an older one.
const (
	MsgContentionTimeout = "Too much contention on these documents. Please try again."
	MsgContentionWounded = "Aborted due to cross-transaction contention. This occurs when multiple transactions " +
		"attempt to access the same data, requiring Firestore to abort at least one in order to enforce serializability."
)

// IndexEntryTooLarge is INVALID_ARGUMENT "Index entry is too large." with the
// google.rpc.ErrorInfo production attaches (f0-errors.json, name_bytes_6127; recorded on REST,
// the same details assumed on gRPC).
func IndexEntryTooLarge() *Error {
	return InvalidArgument("Index entry is too large.").withDetails(&errdetails.ErrorInfo{
		Reason: "INDEX_ENTRY_TOO_LARGE",
		Domain: "firestore.googleapis.com",
	})
}

// IndexError is FAILED_PRECONDITION (HTTP 400) with the needs-index message of package index,
// byte for byte (indexes-105.json, f0-explain.json). REST carries no details (as recorded); gRPC
// carries the console link as a google.rpc.Help, which the Go and Node clients decode (ADR-027
// §1.1). The detail's form, one link with no description, is provisional: production's gRPC
// answer is recorded in F3.
func IndexError(e *index.Error) *Error {
	err := NewError(Code(index.GRPCCode), e.Message)
	if url := indexURL(e.Message); url != "" {
		err = err.WithGRPC(e.Message, &errdetails.Help{Links: []*errdetails.Help_Link{{Url: url}}})
	}
	return err
}

// indexURL is the console link in a needs-index message: from its scheme to the first space or
// newline (template 2 goes on after one), or to the end.
func indexURL(msg string) string {
	i := strings.Index(msg, "https://console.firebase.google.com/")
	if i < 0 {
		return ""
	}
	u := msg[i:]
	if j := strings.IndexAny(u, " \n"); j >= 0 {
		u = u[:j]
	}
	return u
}

// ErrMissingResourceField is production's answer to a request whose database field is empty,
// whatever google-cloud-resource-prefix says (f1-wire-errors.json grpc.database_field.empty).
var ErrMissingResourceField = InvalidArgument("Invalid resource field value in the request.")

// PermissionDenied is PERMISSION_DENIED with production's text for a request the caller may
// not make (f1-resource-names.json, rest.get.invalid_api_key): the binding error for a request
// whose own database is not of the caller's project (ADR-029 §3 rule 6).
func PermissionDenied() *Error {
	return NewError(CodePermissionDenied, "Missing or insufficient permissions.")
}

func (e *Error) withDetails(details ...proto.Message) *Error {
	e.Details = append(e.Details, details...)
	return e
}
