package api

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/core"
	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/wire"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/longrunningpb"
)

// The Admin API (google.firestore.admin.v1.FirestoreAdmin and google.longrunning.Operations)
// as firebase-tools and gcloud call it (ADR-025 §4.5, ADR-028 §2): the databases, read alone
// and only '(default)' exists (named databases come in v0.4.x); the composite indexes; the
// single-field configurations (fields) and their TTL policies; and the operations that report
// index builds and field updates. Every method is an admin's (admitAs), and a database that
// does not exist is the Admin API's NOT_FOUND text (wire.MissingDatabaseAdmin). The other
// methods of the two services answer UNIMPLEMENTED from the transports.
//
// The answers to reads are production's, byte for byte (f3-admin.json, an owner's GETs on a
// project with no index or override); what only a write shows (creating, deleting, updating,
// the refusals of a write) is provisional, as is every text not cited from it.

// adminName is an Admin API resource name split: projects/{p}/databases/{d}, then
// collectionGroups/{group} with indexes[/{id}] or fields[/{id}] under it, or operations[/{id}].
type adminName struct {
	db    wire.Database
	group string // the collection group; "" when the name has none
	kind  string // "indexes", "fields" or "operations"; "" for the database or a group
	id    string // what follows kind: an index id, a field path (which may hold '/'), an operation id
	// hasID tells kind alone (a collection: …/indexes) from kind and an id.
	hasID bool
}

// parseAdminName splits name, or refuses it with wire.InvalidResourceName.
func parseAdminName(name string) (adminName, error) {
	rest, ok := strings.CutPrefix(name, "projects/")
	if !ok {
		return adminName{}, wire.InvalidResourceName(name)
	}
	project, rest, _ := strings.Cut(rest, "/")
	rest, ok = strings.CutPrefix(rest, "databases/")
	if !ok || project == "" {
		return adminName{}, wire.InvalidResourceName(name)
	}
	dbID, rest, _ := strings.Cut(rest, "/")
	db, err := wire.ParseDatabaseName("projects/" + project + "/databases/" + dbID)
	if err == nil && !wire.ValidDatabaseID(dbID) {
		err = wire.InvalidResourceName(name)
	}
	if err != nil {
		if dbID == "" {
			return adminName{}, wire.InvalidResourceName(name)
		}
		// A database id no database may have does not exist, as the Admin API words it
		// (f1-wire-errors.json admin.database_get_bad_id: Upper-case).
		return adminName{}, wire.MissingDatabaseAdmin(wire.Database{Project: project, ID: dbID})
	}
	n := adminName{db: db}
	if rest == "" {
		return n, nil
	}
	if after, ok := strings.CutPrefix(rest, "collectionGroups/"); ok {
		n.group, rest, _ = strings.Cut(after, "/")
		if n.group == "" {
			return adminName{}, wire.InvalidResourceName(name)
		}
		if rest == "" {
			return n, nil
		}
	}
	kind, id, hasID := strings.Cut(rest, "/")
	switch {
	case kind == "operations" && n.group != "", (kind == "indexes" || kind == "fields") && n.group == "",
		kind != "operations" && kind != "indexes" && kind != "fields", hasID && id == "":
		return adminName{}, wire.InvalidResourceName(name)
	}
	n.kind, n.id, n.hasID = kind, id, hasID
	return n, nil
}

// adminResource parses name as one of kind's (an id after it), or kind's collection when
// collection is set; any other form is wire.InvalidResourceName.
func adminResource(name, kind string, collection bool) (adminName, error) {
	n, err := parseAdminName(name)
	if err != nil {
		return adminName{}, err
	}
	if n.kind != kind || n.hasID == collection {
		return adminName{}, wire.InvalidResourceName(name)
	}
	return n, nil
}

// groupParent parses a collection group's name, the parent of its indexes and fields.
func groupParent(parent string) (adminName, error) {
	n, err := parseAdminName(parent)
	if err != nil {
		return adminName{}, err
	}
	if n.group == "" || n.kind != "" {
		return adminName{}, wire.InvalidResourceName(parent)
	}
	return n, nil
}

// checkGroup holds a collection group to a collection id: "-" (every group) only where a list
// allows it, __default__ (the database's default) only for fields.
func checkGroup(group string, list, fields bool) error {
	switch {
	case group == "-":
		if !list {
			return wire.InvalidArgument(`The collection group "-" names every group; it is allowed in a list alone.`)
		}
		return nil
	case group == index.DefaultCollectionGroup:
		if !fields {
			return wire.InvalidArgument("The collection group __default__ has field configurations alone.")
		}
		return nil
	}
	return wire.CheckID(group, false)
}

// primaryOf is the primary of an Admin request: the database its name parses to.
func primaryOf(n adminName, err error) func() (wire.Database, error) {
	return func() (wire.Database, error) { return n.db, err }
}

// admitAdmin admits an Admin API request to the database n names: an admin's, to a database
// that exists.
func (s *Service) admitAdmin(ctx context.Context, c Call, req proto.Message, n adminName, nerr error) (admitted, error) {
	a, err := s.admitAs(ctx, c, req, primaryOf(n, nerr), true)
	if err != nil {
		return admitted{}, err
	}
	if _, err := s.o.Store.Database(ctx, a.db); err != nil {
		return admitted{}, s.failAdmin(ctx, err)
	}
	return a, nil
}

// failAdmin is fail on the Admin API, whose missing database has a text of its own.
func (s *Service) failAdmin(ctx context.Context, err error) error {
	if nf, ok := errors.AsType[*core.DatabaseNotFoundError](err); ok {
		return wire.MissingDatabaseAdmin(wire.Database{Project: nf.ProjectID, ID: nf.DatabaseID})
	}
	return s.fail(ctx, err)
}

// GetDatabase returns '(default)', as firebase deploy reads it first (checkDatabaseType); any
// other database is NOT_FOUND with the Admin API's recorded text (f0-missing-database.json
// admin.getDatabase).
func (s *Service) GetDatabase(ctx context.Context, c Call, req *adminpb.GetDatabaseRequest) (*adminpb.Database, error) {
	n, err := parseAdminName(req.GetName())
	if err == nil && n.group+n.kind != "" {
		err = wire.InvalidResourceName(req.GetName())
	}
	a, err := s.admitAs(ctx, c, req, primaryOf(n, err), true)
	if err != nil {
		return nil, err
	}
	d, err := s.o.Store.Database(ctx, a.db)
	if err != nil {
		return nil, s.failAdmin(ctx, err)
	}
	return databaseProto(d, time.Now()), nil
}

// ListDatabases lists a project's databases: '(default)' alone in v0.4.0.
func (s *Service) ListDatabases(ctx context.Context, c Call, req *adminpb.ListDatabasesRequest) (*adminpb.ListDatabasesResponse, error) {
	project, ok := strings.CutPrefix(req.GetParent(), "projects/")
	primary := func() (wire.Database, error) {
		if !ok || project == "" || strings.Contains(project, "/") {
			return wire.Database{}, wire.InvalidResourceName(req.GetParent())
		}
		return wire.Database{Project: project}, nil
	}
	a, err := s.admitAs(ctx, c, req, primary, true)
	if err != nil {
		return nil, err
	}
	db := a.db
	db.ID = core.DefaultDatabase
	d, err := s.o.Store.Database(ctx, db)
	if err != nil {
		return nil, s.failAdmin(ctx, err)
	}
	return &adminpb.ListDatabasesResponse{Databases: []*adminpb.Database{databaseProto(d, time.Now())}}, nil
}

// databaseProto is a database as the Admin API shows it, with the members production's
// (default) has (f3-admin.json database.get): App Engine integration off, the free tier, and
// real-time updates on.
func databaseProto(d core.Database, now time.Time) *adminpb.Database {
	return &adminpb.Database{
		Name:                          d.Name(),
		Uid:                           d.UID,
		CreateTime:                    timestamppb.New(d.CreateTime),
		UpdateTime:                    timestamppb.New(d.UpdateTime),
		LocationId:                    d.LocationID,
		Type:                          adminpb.Database_DatabaseType(adminpb.Database_DatabaseType_value[d.Type]),
		ConcurrencyMode:               adminpb.Database_ConcurrencyMode(adminpb.Database_ConcurrencyMode_value[d.ConcurrencyMode]),
		VersionRetentionPeriod:        durationpb.New(d.VersionRetention),
		EarliestVersionTime:           timestamppb.New(d.EarliestVersionTime(now).Truncate(time.Microsecond)),
		AppEngineIntegrationMode:      adminpb.Database_DISABLED,
		PointInTimeRecoveryEnablement: adminpb.Database_PointInTimeRecoveryEnablement(adminpb.Database_PointInTimeRecoveryEnablement_value["POINT_IN_TIME_RECOVERY_"+d.PointInTimeRecovery]),
		DeleteProtectionState:         adminpb.Database_DeleteProtectionState(adminpb.Database_DeleteProtectionState_value["DELETE_PROTECTION_"+d.DeleteProtection]),
		DatabaseEdition:               adminpb.Database_DatabaseEdition(adminpb.Database_DatabaseEdition_value[d.Edition]),
		FreeTier:                      new(true),
		RealtimeUpdatesMode:           adminpb.RealtimeUpdatesMode_REALTIME_UPDATES_MODE_ENABLED,
		Etag:                          d.Etag,
	}
}

// CreateIndex creates a composite index of the parent's collection group and returns its
// operation, not done until the index is READY (ADR-025 §3); the same index twice is
// ALREADY_EXISTS, which firebase-tools never sends (it lists first).
func (s *Service) CreateIndex(ctx context.Context, c Call, req *adminpb.CreateIndexRequest) (*longrunningpb.Operation, error) {
	n, err := groupParent(req.GetParent())
	a, err := s.admitAdmin(ctx, c, req, n, err)
	if err != nil {
		return nil, err
	}
	if err := checkGroup(n.group, false, false); err != nil {
		return nil, err
	}
	if req.GetIndex() == nil {
		return nil, wire.InvalidArgument("The request has no index.")
	}
	op, err := s.o.Store.CreateIndex(ctx, a.db, n.group, req.GetIndex())
	if err != nil {
		return nil, s.failAdmin(ctx, err)
	}
	return op, nil
}

// ListIndexes lists the composite indexes of a collection group, or of every group under
// collectionGroups/-, as firebase-tools lists them before a deploy and for firestore:indexes.
func (s *Service) ListIndexes(ctx context.Context, c Call, req *adminpb.ListIndexesRequest) (*adminpb.ListIndexesResponse, error) {
	n, err := groupParent(req.GetParent())
	a, err := s.admitAdmin(ctx, c, req, n, err)
	if err != nil {
		return nil, err
	}
	if err := checkGroup(n.group, true, false); err != nil {
		return nil, err
	}
	if req.GetFilter() != "" {
		return nil, errIndexFilter
	}
	if err := wholeList(req.GetPageSize(), req.GetPageToken()); err != nil {
		return nil, err
	}
	all, err := s.o.Store.ListIndexes(ctx, a.db, n.group)
	if err != nil {
		return nil, s.failAdmin(ctx, err)
	}
	return &adminpb.ListIndexesResponse{Indexes: all}, nil
}

// GetIndex returns a composite index.
func (s *Service) GetIndex(ctx context.Context, c Call, req *adminpb.GetIndexRequest) (*adminpb.Index, error) {
	n, err := adminResource(req.GetName(), "indexes", false)
	a, err := s.admitAdmin(ctx, c, req, n, err)
	if err != nil {
		return nil, err
	}
	if err := checkIndex(n); err != nil {
		return nil, err
	}
	ix, err := s.o.Store.GetIndex(ctx, a.db, n.group, n.id)
	if err != nil {
		return nil, s.failAdmin(ctx, err)
	}
	return ix, nil
}

// DeleteIndex deletes a composite index (firebase deploy --force, or after its prompt).
func (s *Service) DeleteIndex(ctx context.Context, c Call, req *adminpb.DeleteIndexRequest) (*emptypb.Empty, error) {
	n, err := adminResource(req.GetName(), "indexes", false)
	a, err := s.admitAdmin(ctx, c, req, n, err)
	if err != nil {
		return nil, err
	}
	if err := checkIndex(n); err != nil {
		return nil, err
	}
	if err := s.o.Store.DeleteIndex(ctx, a.db, n.group, n.id); err != nil {
		return nil, s.failAdmin(ctx, err)
	}
	return &emptypb.Empty{}, nil
}

// GetField returns a field's single-field configuration: its own or the one it inherits. The
// path may be quoted (`a b`), and its name in the answer is in canonical form (f3-admin.json
// fields.get_*).
func (s *Service) GetField(ctx context.Context, c Call, req *adminpb.GetFieldRequest) (*adminpb.Field, error) {
	n, err := adminResource(req.GetName(), "fields", false)
	a, err := s.admitAdmin(ctx, c, req, n, err)
	if err != nil {
		return nil, err
	}
	// collectionGroups/-/fields/v is answered as a group no field of which is configured
	// (f3-admin.json fields.get_dash_group).
	if err := checkGroup(n.group, true, true); err != nil {
		return nil, err
	}
	f, err := s.o.Store.GetField(ctx, a.db, n.group, n.id)
	if err != nil {
		return nil, s.failAdmin(ctx, err)
	}
	return f, nil
}

// ListFields lists the fields of a collection group, or of every group, that the filter
// selects (fieldFilter): firebase-tools' indexConfig.usesAncestorConfig=false OR ttlConfig:*,
// gcloud's indexConfig.usesAncestorConfig:false, or either term alone. Production refuses a
// listing without one of them (f3-admin.json fields.list_no_filter, list_bad_filter), and lists
// __default__/fields/* among the fields that do not use their ancestor's configuration.
func (s *Service) ListFields(ctx context.Context, c Call, req *adminpb.ListFieldsRequest) (*adminpb.ListFieldsResponse, error) {
	n, err := groupParent(req.GetParent())
	a, err := s.admitAdmin(ctx, c, req, n, err)
	if err != nil {
		return nil, err
	}
	if err := checkGroup(n.group, true, true); err != nil {
		return nil, err
	}
	keep, err := fieldFilter(req.GetFilter())
	if err != nil {
		return nil, err
	}
	if err := wholeList(req.GetPageSize(), req.GetPageToken()); err != nil {
		return nil, err
	}
	fields, err := s.o.Store.ListFields(ctx, a.db, n.group)
	if err != nil {
		return nil, s.failAdmin(ctx, err)
	}
	var out []*adminpb.Field
	for _, f := range fields {
		if keep(f) {
			out = append(out, f)
		}
	}
	return &adminpb.ListFieldsResponse{Fields: out}, nil
}

// fieldFilter reads ListFields' filter: terms joined by OR, each indexConfig.usesAncestorConfig
// compared with false (by = or :) or ttlConfig:* (any TTL policy), in JSON or proto names. Any
// other filter, the empty one included, is errFieldFilter.
func fieldFilter(filter string) (func(*adminpb.Field) bool, error) {
	if strings.TrimSpace(filter) == "" {
		return nil, errFieldFilter
	}
	var own, ttl bool
	for term := range strings.SplitSeq(filter, " OR ") {
		switch strings.Join(strings.Fields(term), "") {
		case "indexConfig.usesAncestorConfig=false", "indexConfig.usesAncestorConfig:false",
			"index_config.uses_ancestor_config=false", "index_config.uses_ancestor_config:false":
			own = true
		case "ttlConfig:*", "ttl_config:*":
			ttl = true
		default:
			return nil, errFieldFilter
		}
	}
	return func(f *adminpb.Field) bool {
		return own && !f.GetIndexConfig().GetUsesAncestorConfig() || ttl && f.GetTtlConfig() != nil
	}, nil
}

// UpdateField changes a field's configuration (the update mask names index_config, ttl_config
// or both; empty, both): an index_config lists the field's indexes, none for an exemption, and
// a ttl_config sets its TTL policy; a part the mask names and the field lacks returns to the
// ancestor's, so firebase-tools' PATCH with {} removes an override. The change is queued: the
// operation returned is not done (core.PGStore.UpdateField).
func (s *Service) UpdateField(ctx context.Context, c Call, req *adminpb.UpdateFieldRequest) (*longrunningpb.Operation, error) {
	n, err := adminResource(req.GetField().GetName(), "fields", false)
	a, err := s.admitAdmin(ctx, c, req, n, err)
	if err != nil {
		return nil, err
	}
	if err := checkGroup(n.group, false, true); err != nil {
		return nil, err
	}
	op, err := s.o.Store.UpdateField(ctx, a.db, n.group, req.GetField(), req.GetUpdateMask().GetPaths())
	if err != nil {
		return nil, s.failAdmin(ctx, err)
	}
	return op, nil
}

// GetOperation returns an operation of the database.
func (s *Service) GetOperation(ctx context.Context, c Call, req *longrunningpb.GetOperationRequest) (*longrunningpb.Operation, error) {
	n, err := adminResource(req.GetName(), "operations", false)
	a, err := s.admitAdmin(ctx, c, req, n, err)
	if err != nil {
		return nil, err
	}
	op, err := s.o.Store.GetOperation(ctx, a.db, n.id)
	if ce, ok := errors.AsType[*core.Error](err); ok && ce.Code == core.NotFound {
		return nil, operationNotFound(n.id)
	}
	if err != nil {
		return nil, s.failAdmin(ctx, err)
	}
	return op, nil
}

// ListOperations lists the database's operations, oldest first: its name is the database's
// (GET /v1/projects/{p}/databases/{d}/operations) or its operations collection's.
func (s *Service) ListOperations(ctx context.Context, c Call, req *longrunningpb.ListOperationsRequest) (*longrunningpb.ListOperationsResponse, error) {
	n, err := parseAdminName(req.GetName())
	if err == nil && (n.group != "" || n.kind != "" && (n.kind != "operations" || n.hasID)) {
		err = wire.InvalidResourceName(req.GetName())
	}
	a, err := s.admitAdmin(ctx, c, req, n, err)
	if err != nil {
		return nil, err
	}
	if req.GetFilter() != "" {
		return nil, wire.InvalidArgument("A filter on the listing of operations is not supported: " + strconv.Quote(req.GetFilter()) + ".")
	}
	all, err := s.o.Store.ListOperations(ctx, a.db)
	if err != nil {
		return nil, s.failAdmin(ctx, err)
	}
	page, next, err := adminPage(all, req.GetPageSize(), req.GetPageToken())
	if err != nil {
		return nil, err
	}
	return &longrunningpb.ListOperationsResponse{Operations: page, NextPageToken: next}, nil
}

// adminPage pages ListOperations, which takes a page size (f3-admin.json
// operations.list_page_size_1): page_size items from the token's offset (all when 0), and the
// token of the next page when more follow. A token is the offset, opaque to clients
// (provisional).
func adminPage[T any](all []T, size int32, token string) ([]T, string, error) {
	if size < 0 {
		return nil, "", wire.InvalidArgument("page_size must not be negative.")
	}
	from := 0
	if token != "" {
		b, err := base64.RawURLEncoding.DecodeString(token)
		o, ok := strings.CutPrefix(string(b), "o")
		n, aerr := strconv.Atoi(o)
		if err != nil || !ok || aerr != nil || n <= 0 || n > len(all) {
			return nil, "", errAdminPageTok
		}
		from = n
	}
	to := len(all)
	if size > 0 {
		to = min(to, from+int(size))
	}
	next := ""
	if to < len(all) {
		next = base64.RawURLEncoding.EncodeToString([]byte("o" + strconv.Itoa(to)))
	}
	return all[from:to], next, nil
}

// The listings' refusals (f3-admin.json indexes.list_filter, indexes.list_page_size_1,
// fields.list_page_size_1, fields.list_no_filter). A page token is provisional: production
// issues none, since a listing is one page.
var (
	errIndexFilter  = wire.InvalidArgument("Index filters not supported.")
	errPageSize     = wire.InvalidArgument("Invalid page size. Only 0 is supported.")
	errFieldFilter  = wire.InvalidArgument("ListFieldsRequest.filter must include at least fields that do not use their ancestor configuration, or fields with TTLs.")
	errAdminPageTok = wire.InvalidArgument("Invalid page token.")
)

// wholeList holds ListIndexes and ListFields to production's one page: page_size 0, no token.
func wholeList(size int32, token string) error {
	if size != 0 {
		return errPageSize
	}
	if token != "" {
		return errAdminPageTok
	}
	return nil
}

// checkIndex checks an index's name: an id of the form this server issues (FormatIndexID),
// else production's INVALID_ARGUMENT, whatever the group (f3-admin.json indexes.get_missing,
// get_missing_word, get_dash_group); then a group an index may have.
func checkIndex(n adminName) error {
	if _, ok := core.ParseIndexID(n.id); !ok {
		return wire.InvalidArgument(`Invalid index resource id "` + n.id + `".`)
	}
	return checkGroup(n.group, false, false)
}

// operationNotFound is production's answer to an operation that does not exist, which has two
// texts (f3-admin.json operations.get_missing, get_missing_word): an id of an operation's form,
// base64 of 16 bytes or more as this server's and production's are, is "Requested entity was
// not found."; any other "Operation does not exist". Where production draws the line between
// them is inferred from the two recorded ids.
func operationNotFound(id string) error {
	for _, enc := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := enc.DecodeString(id); err == nil && len(b) >= 16 {
			return wire.NewError(wire.CodeNotFound, "Requested entity was not found.")
		}
	}
	return wire.NewError(wire.CodeNotFound, "Operation does not exist")
}

// ListBackupSchedules lists the database's backup schedules: none, as production answers a
// database without any (f3-admin.json backup_schedules.list). Backups are not served.
func (s *Service) ListBackupSchedules(ctx context.Context, c Call, req *adminpb.ListBackupSchedulesRequest) (*adminpb.ListBackupSchedulesResponse, error) {
	n, err := parseAdminName(req.GetParent())
	if err == nil && n.group+n.kind != "" {
		err = wire.InvalidResourceName(req.GetParent())
	}
	if _, err := s.admitAdmin(ctx, c, req, n, err); err != nil {
		return nil, err
	}
	return &adminpb.ListBackupSchedulesResponse{}, nil
}
