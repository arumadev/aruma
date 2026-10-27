package api

import (
	"cmp"
	"context"
	"errors"
	"log/slog"

	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/platform/apierr"
	"github.com/arumadev/aruma/internal/platform/authn"
	"github.com/arumadev/aruma/internal/platform/httpx"
	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/listen"
	"github.com/arumadev/aruma/internal/services/documents/wire"
)

// Projects resolves a project id, as resource names carry it, to its number, which keys every
// row. tenancy/core implements it. A missing or deleted project is NOT_FOUND.
type Projects interface {
	ProjectNumber(ctx context.Context, id string) (int64, error)
}

// Options configure a Service.
type Options struct {
	// Store is the Document Database's storage.
	Store core.Store
	// Auth resolves and admits callers (ADR-029).
	Auth *authn.Documents
	// Projects resolves the project of a request's database.
	Projects Projects
	// DevOpenRules is documents.dev_open_rules: callers that are not admins reach the data from
	// a loopback peer. Config accepts it only on a loopback deployment (ADR-025 §8).
	DevOpenRules bool
	// IndexEnforcement is documents.index_enforcement, the composite-index enforcement of every
	// database that sets none of its own (ADR-025 §4.4); the zero value is index.Enforce.
	IndexEnforcement index.Mode
	// ReferenceOnly serves every query on the reference path, never from an index: the
	// arbiter the planner's differential tests compare it with (ADR-025 §1).
	ReferenceOnly bool
	// Listen is the hub the Listen streams subscribe to (ADR-026 §2); without it Listen is
	// UNIMPLEMENTED.
	Listen *listen.Hub
	Logger *slog.Logger
}

// Service serves the Document Database's methods for every transport. It is safe for
// concurrent use.
type Service struct {
	o   Options
	log *slog.Logger

	// Tests' hooks into the index paths (export_test.go): indexBatch, when set, is the results
	// of every batch; afterBatch runs after a batch is sent, before the next snapshot begins;
	// observePeak is told the most bytes of documents a query held.
	indexBatch  int
	afterBatch  func()
	observePeak func(int)
}

// New returns the service.
func New(o Options) *Service {
	return &Service{o: o, log: cmp.Or(o.Logger, slog.Default())}
}

// Call is how a request arrived, besides its message.
type Call struct {
	// Authorization is the Authorization header (or gRPC metadata) as sent.
	Authorization string
	// Transport is how the request reached Aruma (authn.TransportOf).
	Transport authn.Transport
	// ResourcePrefix is google-cloud-resource-prefix, projects/{p}/databases/{d}, as sent. No F1
	// method reads it: a request's database field stands alone (databaseOf).
	ResourcePrefix string
	// RequestParams is x-goog-request-params, as sent: a Listen stream's database must be the
	// one it names, when google-cloud-resource-prefix does not.
	RequestParams string
}

// admitted is a request admitted to its database.
type admitted struct {
	db     core.DB
	wdb    wire.Database
	caller *authn.Caller
	// by is the caller as transactions know it (principalOf).
	by core.Principal
}

// admit runs the steps every request takes before its own (package doc): the caller, the
// binding of every name in req to the database primary names, the project, the admission and
// the rules' default. primary is the name the request's database comes from; empty, the
// resource prefix stands for it.
func (s *Service) admit(ctx context.Context, c Call, req proto.Message, primary func() (wire.Database, error)) (admitted, error) {
	return s.admitAs(ctx, c, req, primary, false)
}

// admitAs is admit; with adminOnly, the Admin API's: the caller must be an admin, whatever
// documents.dev_open_rules says, and is refused before its names are looked at
// (wire.ErrAdminUnauthenticated without a credential, wire.ErrAdminPermissionDenied for an end
// user).
func (s *Service) admitAs(ctx context.Context, c Call, req proto.Message, primary func() (wire.Database, error), adminOnly bool) (admitted, error) {
	caller, err := s.o.Auth.Resolve(ctx, c.Authorization, c.Transport)
	if err != nil {
		return admitted{}, s.fail(ctx, err)
	}
	if adminOnly && caller.Role != authn.RoleAdmin {
		if caller.Role == authn.RoleAnonymous {
			return admitted{}, wire.ErrAdminUnauthenticated
		}
		return admitted{}, wire.ErrAdminPermissionDenied
	}
	first, err := primary()
	if err != nil {
		return admitted{}, err
	}
	// first is the request's database: every other name must be of it.
	wdb, err := wire.Bind(req, first)
	if err != nil {
		return admitted{}, err
	}
	number, err := s.o.Projects.ProjectNumber(ctx, wdb.Project)
	switch {
	case apierr.CodeOf(err) == apierr.NotFound:
		// A project that does not exist is no more the caller's than another's: production
		// answers a project the caller may not use alike (ADR-029 §3 rule 6).
		return admitted{}, wire.PermissionDenied()
	case err != nil:
		return admitted{}, s.fail(ctx, err)
	}
	if err := s.o.Auth.Admit(ctx, caller, number); err != nil {
		return admitted{}, s.fail(ctx, err)
	}
	if !s.permitted(caller, c.Transport) {
		return admitted{}, wire.PermissionDenied()
	}
	return admitted{
		db:     core.DB{Project: number, ProjectID: wdb.Project, ID: wdb.ID},
		wdb:    wdb,
		caller: caller,
		by:     principalOf(caller),
	}, nil
}

// permitted is the rules' default until F5 (ADR-025 §8): admins only, or anyone on this machine
// with documents.dev_open_rules.
func (s *Service) permitted(caller *authn.Caller, t authn.Transport) bool {
	if caller.Role == authn.RoleAdmin {
		return true
	}
	return s.o.DevOpenRules && t.Peer.IsValid() && t.Peer.IsLoopback()
}

// databaseOf is the primary of a request whose message names its database by a field. The
// field is required: production refuses it empty even when google-cloud-resource-prefix names
// the database (f1-wire-errors.json grpc.database_field.empty).
func databaseOf(database string) func() (wire.Database, error) {
	return func() (wire.Database, error) {
		if database == "" {
			return wire.Database{}, wire.ErrMissingResourceField
		}
		return wire.ParseDatabaseName(database)
	}
}

// documentOf and parentOf are the primary of a request whose database is in a document name or
// a parent name.
func documentOf(name string) func() (wire.Database, error) {
	return func() (wire.Database, error) {
		n, err := wire.ParseDocumentName(name)
		return n.Database, err
	}
}

func parentOf(name string) func() (wire.Database, error) {
	return func() (wire.Database, error) {
		n, err := wire.ParseParentName(name)
		return n.Database, err
	}
}

// fail maps an error to the *wire.Error the client is told of, with production's texts: core's
// errors by their code, the missing database by surface (wire.MissingDatabase), authn's by
// theirs. Anything else is the server's own and is logged: the client sees INTERNAL.
func (s *Service) fail(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if we, ok := errors.AsType[*wire.Error](err); ok {
		return we
	}
	if nf, ok := errors.AsType[*core.DatabaseNotFoundError](err); ok {
		return wire.MissingDatabase(wire.Database{Project: nf.ProjectID, ID: nf.DatabaseID})
	}
	// The core errors production answers with details (f0-errors.json name_bytes_6127,
	// f3-index-entries.json fields.n_20001).
	if errors.Is(err, core.ErrIndexEntryTooLarge) {
		return wire.IndexEntryTooLarge()
	}
	if te, ok := errors.AsType[*core.TooManyIndexEntriesError](err); ok {
		return wire.TooManyIndexEntries(te.DocPath())
	}
	if ce, ok := errors.AsType[*core.Error](err); ok {
		return wire.NewError(wire.Code(ce.Code), ce.Message)
	}
	if ae, ok := errors.AsType[*apierr.Error](err); ok && ae.Code != apierr.Internal {
		return wire.NewError(wire.Code(ae.Code), ae.Message)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return wire.From(err)
	}
	s.log.Error("documents", "error", err, "request_id", httpx.RequestID(ctx))
	return wire.Internal()
}
