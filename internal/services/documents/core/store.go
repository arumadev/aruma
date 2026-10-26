package core

import (
	"context"
	"time"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

// Store is the Document Database's storage, as the transports and the later phases use it. Every
// call names the database it acts on; a database other than '(default)' that does not exist is a
// *DatabaseNotFoundError, which carries the three texts production answers with. Every read
// returns the read_time it answered at: the commit_time of the last commit it saw, or the
// ReadTime it was given. A failure is an *Error (or a *DatabaseNotFoundError) whose Code each
// surface maps to its status; any other error is the server's own (INTERNAL).
//
// Requests reach Store already checked by the transport: resource names parsed and bound to db,
// ids and names within production's limits, values valid (value.Validate), read times whole
// microseconds. Store checks what only the stored data decides: preconditions, the size and
// validity of a document after its update mask, and read times against the database's window.
type Store interface {
	// Database returns a database's settings; '(default)' is created on first use.
	Database(ctx context.Context, db DB) (Database, error)
	// Get returns a document, or an *Error with code NotFound and production's text.
	Get(ctx context.Context, db DB, p Path, o ReadOptions) (Document, time.Time, error)
	// BatchGet returns the documents in the order asked; one that does not exist has its Path
	// alone (Exists false). A path may be asked twice.
	BatchGet(ctx context.Context, db DB, paths []Path, o ReadOptions) ([]Document, time.Time, error)
	// ListDocuments lists a collection's documents in name order, or every collection's under a
	// parent; with ShowMissing, the documents that do not exist but have descendants too.
	ListDocuments(ctx context.Context, db DB, r ListRequest) (ListResult, error)
	// ListCollectionIDs lists the ids of the collections directly under a document, or under
	// the root, in order, whether or not that document exists.
	ListCollectionIDs(ctx context.Context, db DB, r ListCollectionIDsRequest) (CollectionIDs, error)
	// Commit applies writes atomically, in order, as one commit: every precondition holds and
	// every write applies, or none does.
	Commit(ctx context.Context, db DB, writes []Write) (CommitResult, error)
}

// Transactions is the transactions' part of the store (ADR-025 §7; txn.go). Reads in a
// transaction are Store's, with ReadOptions.Transaction. PGStore implements it; the handlers'
// wave folds it into Store.
//
// A new_transaction on a read (BatchGetDocuments, RunQuery) is BeginTransaction, then the read
// with the id, and Rollback if the read fails; the id goes in the first response.
type Transactions interface {
	// BeginTransaction begins a transaction for by and returns its id. A read-write one is
	// pessimistic for an admin, unless it asks for ConcurrencyOptimistic, and optimistic for
	// anyone else, who must ask for it (ErrOptimisticRequired). A read-only one's ReadTime is
	// checked as a read's. Over the open transactions' bound it is ABORTED.
	BeginTransaction(ctx context.Context, db DB, by Principal, o TransactionOptions) ([]byte, error)
	// CommitTransaction applies writes as Commit does, in the transaction, which it ends
	// whatever the outcome. A pessimistic one waits for its exclusive locks; an optimistic one
	// is ABORTED (ErrReadsChanged) if anything it read changed. A read-only one takes no writes
	// (ErrReadOnlyWrite).
	CommitTransaction(ctx context.Context, db DB, t TransactionRef, writes []Write) (CommitResult, error)
	// Rollback ends a transaction and frees its locks. It accepts a transaction that is over
	// or unknown here (Node sends it without care, ADR-025 §7.5), not a forged id.
	Rollback(ctx context.Context, db DB, t TransactionRef) error
}

// DefaultDatabase is the database every project has, and the only one in v0.4.0.
const DefaultDatabase = "(default)"

// DB names one database of one project.
type DB struct {
	// Project is the project number, which keys every row.
	Project int64
	// ProjectID is the project's id, which resource names and production's messages carry.
	ProjectID string
	// ID is the database id: DefaultDatabase, or a named database's.
	ID string
}

// Name returns the database's resource name: projects/{project_id}/databases/{database_id}.
func (db DB) Name() string { return "projects/" + db.ProjectID + "/databases/" + db.ID }

// DocumentName returns the resource name of a document, collection or (for the empty path) the
// documents root: projects/{project_id}/databases/{database_id}/documents[/{path}].
func (db DB) DocumentName(p Path) string {
	if len(p) == 0 {
		return db.Name() + "/documents"
	}
	return db.Name() + "/documents/" + p.String()
}

// Database is a database's settings, as the Admin API's Database resource shows them.
type Database struct {
	DB
	UID                 string
	CreateTime          time.Time
	UpdateTime          time.Time
	Etag                string
	Type                string // FIRESTORE_NATIVE
	Edition             string // STANDARD
	LocationID          string
	ConcurrencyMode     string // PESSIMISTIC
	DeleteProtection    string // DISABLED or ENABLED
	PointInTimeRecovery string // DISABLED or ENABLED
	// VersionRetention is how far back a read_time may reach: earliestVersionTime is now minus it.
	VersionRetention time.Duration
	// IndexEnforcement overrides the server's composite-index enforcement; "" keeps it.
	IndexEnforcement string
}

// Document is a document as stored: its fields (a map value) and its times, which are whole
// microseconds. A document that does not exist has its Path alone.
type Document struct {
	Path       Path
	Fields     value.Value
	CreateTime time.Time
	UpdateTime time.Time
}

// Exists reports whether the document exists. A "missing" document of ListDocuments with
// ShowMissing, which has descendants but no fields, does not.
func (d Document) Exists() bool { return !d.UpdateTime.IsZero() }

// ReadOptions selects the state a read sees.
type ReadOptions struct {
	// ReadTime reads the database as it was at that time: at most now, and within the
	// database's version retention (an hour), else an *Error (InvalidArgument for the future,
	// FailedPrecondition for too old). Zero reads the latest commit, whose commit_time is the
	// read_time returned.
	ReadTime time.Time
	// Transaction reads in a transaction (Transactions), with ReadTime zero: a read-only one's
	// snapshot; the latest commit, under shared locks, for a pessimistic one; the latest commit,
	// recorded for its commit to check, for an optimistic one. Get, BatchGet and ListDocuments
	// take it; ListCollectionIDs does not. An id that is not authentic for the database and
	// principal is ErrInvalidTransaction; one of a transaction that is over is
	// ErrTransactionExpired, or, for the call that meets it first, why it ended (ErrWounded,
	// ErrContention).
	Transaction *TransactionRef
}

// ListRequest is a ListDocuments call.
type ListRequest struct {
	// Parent is the document whose collections are listed; nil for the root.
	Parent Path
	// CollectionID is the collection listed; "" lists every collection directly under Parent,
	// which ShowMissing does not allow (production refuses it before this).
	CollectionID string
	// PageSize is the most documents returned; 0 is MaxPageSize.
	PageSize int
	// After resumes a listing after that document (ListResult.Next of the page before).
	After Path
	// ShowMissing lists the documents that do not exist but have descendants too, by name alone.
	ShowMissing bool
	ReadOptions
}

// MaxPageSize bounds a page of ListDocuments and ListCollectionIDs. Production's own bound is not
// recorded; a transport may ask for less.
const MaxPageSize = 1000

// ListResult is a page of ListDocuments.
type ListResult struct {
	Documents []Document
	// Next is the last document of the page when more may follow, else nil.
	Next     Path
	ReadTime time.Time
}

// ListCollectionIDsRequest is a ListCollectionIds call.
type ListCollectionIDsRequest struct {
	// Parent is the document whose collections are listed; nil for the root.
	Parent Path
	// PageSize is the most ids returned; 0 is MaxPageSize.
	PageSize int
	// After resumes a listing after that id.
	After string
	ReadOptions
}

// CollectionIDs is a page of ListCollectionIDs.
type CollectionIDs struct {
	IDs []string
	// More reports that ids may follow the last one.
	More     bool
	ReadTime time.Time
}

// WriteKind is what a write does to its document.
type WriteKind uint8

const (
	// WriteUpdate sets the document's fields, all of them or those of Mask, creating it if
	// it does not exist.
	WriteUpdate WriteKind = iota + 1
	// WriteDelete deletes the document; deleting one that does not exist is no change.
	WriteDelete
	// WriteVerify checks Precondition and changes nothing (Write.verify of the client protos).
	WriteVerify
)

// Write is one write of a commit.
type Write struct {
	Kind WriteKind
	Path Path
	// Fields is the document's new fields (a map value), for WriteUpdate.
	Fields value.Value
	// Masked applies Fields through Mask (an update mask): only the paths it names change,
	// and an empty Mask changes no field. Unmasked, Fields replace the document's.
	Masked bool
	Mask   []value.FieldPath
	// Precondition must hold of the document before this write.
	Precondition Precondition
}

// Precondition is a write's current_document condition; the zero value has none.
type Precondition struct {
	// Exists, when set, requires the document to exist (true) or not (false).
	Exists *bool
	// UpdateTime, when set, requires the document to exist with that update_time.
	UpdateTime time.Time
}

// WriteResult is one write's result.
type WriteResult struct {
	// UpdateTime is the document's update_time after the write: the commit_time when the
	// commit changed the document, else its previous one. Zero for a delete, and for a verify
	// of a document that does not exist.
	UpdateTime time.Time
}

// CommitResult is a commit's outcome.
type CommitResult struct {
	CommitTime time.Time
	// Results has one entry per write, in order.
	Results []WriteResult
	// Seq is the commit's place in the database's commit order; 0 when it changed nothing and
	// so took none.
	Seq int64
}
