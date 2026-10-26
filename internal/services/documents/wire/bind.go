package wire

import (
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/arumadev/aruma/internal/services/documents/wire/pb/firestorepb"
)

// TargetError is Bind's error for a Listen add_target whose names fail. The stream stays open:
// Listen answers TargetChange{REMOVE, [TargetID], cause: Err} (ADR-026 §1.3, ADR-027 §2.2).
type TargetError struct {
	TargetID int32
	Err      *Error
}

func (e *TargetError) Error() string { return e.Err.Error() }

func (e *TargetError) Unwrap() error { return e.Err }

// Bind checks every resource name req carries, before any work (ADR-027 §2.2), and returns the
// database the request works on. Two rules, in the order the names come:
//
//   - The request's database is the first database a name gives: the database field, or the
//     parent or name of a request without one (the REST path's, once the transport has put it in
//     the message). The caller must be entitled to it: its project must be auth.Project, else
//     PermissionDenied (ADR-029 §3 rule 6).
//   - Every other name must be of that database, else production's INVALID_ARGUMENT "The
//     request was for database '{request's}' but was attempting to access database
//     '{the name's}'" (f1-resource-names.json, REST and gRPC alike, for another project or
//     another database of the project).
//
// When auth.ID is set, auth is the request's database already, checked by the caller (a Write
// stream's later messages bind with what its first returned; a REST handler may bind its path
// with BindName first): every name, the first included, must then be of it.
//
// The names walked:
//
//   - database, parent and name, wherever a request has them, and UpdateDocument's
//     document.name;
//   - every write of Commit, BatchWrite and Write: update.name, delete, verify and
//     transform.document;
//   - BatchGetDocuments' documents;
//   - Listen's database, then its add_target's documents or query parent; a target's failure
//     is a *TargetError, so the stream can remove the target and stay open;
//   - on the Admin API and google.longrunning, every string field named name, parent,
//     database or backup at any depth: projects/{p}, projects/{p}/databases/{d}[/…] or
//     projects/{p}/locations/…
//
// Reference values in fields, transforms, filters and cursors are data, never bound: production
// stores a reference to another project as any other value (values-order.json r-other-project).
// A transaction id is bound by its authenticated content, which core checks (ADR-025 §7.5).
// Empty names are skipped: a missing required field is the method's error, not the binding's.
// A name that does not parse is INVALID_ARGUMENT with production's text (ParseName).
func Bind(req proto.Message, auth Database) (Database, error) {
	b := newBinder(auth)
	var err error
	switch r := req.(type) {
	case *firestorepb.GetDocumentRequest:
		err = b.document(r.GetName())
	case *firestorepb.ListDocumentsRequest:
		err = b.parent(r.GetParent())
	case *firestorepb.CreateDocumentRequest:
		err = b.parent(r.GetParent())
	case *firestorepb.UpdateDocumentRequest:
		err = b.document(r.GetDocument().GetName())
	case *firestorepb.DeleteDocumentRequest:
		err = b.document(r.GetName())
	case *firestorepb.BatchGetDocumentsRequest:
		err = b.database(r.GetDatabase())
		for _, d := range r.GetDocuments() {
			if err != nil {
				break
			}
			err = b.document(d)
		}
	case *firestorepb.BeginTransactionRequest:
		err = b.database(r.GetDatabase())
	case *firestorepb.CommitRequest:
		err = b.writes(r.GetDatabase(), r.GetWrites())
	case *firestorepb.BatchWriteRequest:
		err = b.writes(r.GetDatabase(), r.GetWrites())
	case *firestorepb.WriteRequest:
		err = b.writes(r.GetDatabase(), r.GetWrites())
	case *firestorepb.RollbackRequest:
		err = b.database(r.GetDatabase())
	case *firestorepb.RunQueryRequest:
		err = b.parent(r.GetParent())
	case *firestorepb.RunAggregationQueryRequest:
		err = b.parent(r.GetParent())
	case *firestorepb.PartitionQueryRequest:
		err = b.parent(r.GetParent())
	case *firestorepb.ListCollectionIdsRequest:
		err = b.parent(r.GetParent())
	case *firestorepb.ExecutePipelineRequest:
		err = b.database(r.GetDatabase())
	case *firestorepb.ListenRequest:
		if err = b.database(r.GetDatabase()); err == nil {
			if t := r.GetAddTarget(); t != nil {
				if terr := b.target(t); terr != nil {
					err = &TargetError{TargetID: t.GetTargetId(), Err: From(terr)}
				}
			}
		}
	default:
		pkg := req.ProtoReflect().Descriptor().ParentFile().Package()
		if pkg != "google.firestore.admin.v1" && pkg != "google.longrunning" {
			// A request type Bind does not know: refuse rather than let unbound names through.
			return Database{}, Internal()
		}
		err = b.admin(req.ProtoReflect())
	}
	if err != nil {
		return Database{}, err
	}
	return b.req, nil
}

// BindName binds one resource name that is not in a request message, as the request's
// database: the resource part of a REST path, or google-cloud-resource-prefix
// (projects/{p}/databases/{d}) where the message carries no name at all. Production ignores a
// prefix that names another project than the body does (f1-resource-names.json,
// grpc.*.prefix_other_project), so a prefix is never bound against the body's names.
func BindName(name string, auth Database) (Database, error) {
	b := newBinder(auth)
	if err := b.adminName(name); err != nil {
		return Database{}, err
	}
	return b.req, nil
}

// binder binds a request's names: auth is the caller's authority, req the request's database
// once a name has given it.
type binder struct {
	auth Database
	req  Database
}

func newBinder(auth Database) binder { return binder{auth: auth, req: auth} }

func (b *binder) bind(db Database) error {
	if db.ID == "" { // projects/{p} alone: the caller's project, or nothing
		if db.Project != b.auth.Project {
			return PermissionDenied()
		}
		return nil
	}
	if b.req.ID == "" { // the request's own database: the caller must be entitled to it
		if db.Project != b.auth.Project {
			return PermissionDenied()
		}
		b.req = db
		return nil
	}
	if db != b.req {
		return InvalidArgument("The request was for database '" + b.req.Name() +
			"' but was attempting to access database '" + db.Name() + "'")
	}
	return nil
}

func (b *binder) database(s string) error {
	if s == "" {
		return nil
	}
	db, err := ParseDatabaseName(s)
	if err != nil {
		return err
	}
	return b.bind(db)
}

func (b *binder) document(s string) error {
	if s == "" {
		return nil
	}
	n, err := ParseDocumentName(s)
	if err != nil {
		return err
	}
	return b.bind(n.Database)
}

func (b *binder) parent(s string) error {
	if s == "" {
		return nil
	}
	n, err := ParseParentName(s)
	if err != nil {
		return err
	}
	return b.bind(n.Database)
}

func (b *binder) writes(database string, writes []*firestorepb.Write) error {
	if err := b.database(database); err != nil {
		return err
	}
	for _, w := range writes {
		var err error
		switch op := w.GetOperation().(type) {
		case *firestorepb.Write_Update:
			err = b.document(op.Update.GetName())
		case *firestorepb.Write_Delete:
			err = b.document(op.Delete)
		case *firestorepb.Write_Verify:
			err = b.document(op.Verify)
		case *firestorepb.Write_Transform:
			err = b.document(op.Transform.GetDocument())
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (b *binder) target(t *firestorepb.Target) error {
	switch tt := t.GetTargetType().(type) {
	case *firestorepb.Target_Documents:
		for _, d := range tt.Documents.GetDocuments() {
			if err := b.document(d); err != nil {
				return err
			}
		}
	case *firestorepb.Target_Query:
		return b.parent(tt.Query.GetParent())
	}
	return nil
}

// adminNameFields are the fields of Admin API and google.longrunning messages that hold
// resource names.
var adminNameFields = map[protoreflect.Name]bool{"name": true, "parent": true, "database": true, "backup": true}

// admin walks an Admin API or google.longrunning message for its names.
func (b *binder) admin(m protoreflect.Message) error {
	var err error
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsMap():
			if fd.MapValue().Message() != nil {
				v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
					err = b.admin(mv.Message())
					return err == nil
				})
			}
		case fd.IsList():
			if fd.Message() != nil {
				l := v.List()
				for i := 0; i < l.Len() && err == nil; i++ {
					err = b.admin(l.Get(i).Message())
				}
			}
		case fd.Message() != nil:
			err = b.admin(v.Message())
		case fd.Kind() == protoreflect.StringKind && adminNameFields[fd.Name()]:
			err = b.adminName(v.String())
		}
		return err == nil
	})
	return err
}

// adminName binds projects/{p}, projects/{p}/databases/{d}[/…] or projects/{p}/{other}/…
func (b *binder) adminName(s string) error {
	if s == "" {
		return nil
	}
	rest, ok := strings.CutPrefix(s, "projects/")
	if !ok {
		return invalidResourceName(s)
	}
	project, rest, _ := strings.Cut(rest, "/")
	if project == "" {
		return invalidResourceName(s)
	}
	db := Database{Project: project}
	if rest, ok := strings.CutPrefix(rest, "databases/"); ok {
		db.ID, _, _ = strings.Cut(rest, "/")
		if !validDatabaseID(db.ID) {
			return InvalidArgument(`Illegal string "` + db.ID + `" in database.`)
		}
	}
	return b.bind(db)
}

// invalidResourceName is the error for an Admin or header name that is not projects/… (the
// text is provisional).
func invalidResourceName(s string) error {
	return InvalidArgument(`Invalid resource name "` + s + `".`)
}
