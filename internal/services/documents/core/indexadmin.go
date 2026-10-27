package core

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/queries"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/longrunningpb"
)

// The Admin API's index configuration (ADR-025 §3, §4.5), as the transport calls it: composite
// indexes created, listed and deleted; single-field configurations (fields) read and updated;
// and the long-running operations that report their builds. Messages are core's own Admin
// protos (google.firestore.admin.v1), which the transport converts to REST JSON or sends on
// gRPC; resource names are production's: projects/{p}/databases/{d}/collectionGroups/{g}/
// indexes/{id}, …/fields/{path} and projects/{p}/databases/{d}/operations/{id}.
//
// Every text here is provisional: the oracle may not create indexes on the production project
// (ADR-025 §4.2, R21), so none of these answers is recorded.

// IndexLimits bounds a database's index configuration (documents.limits.composite_indexes and
// field_overrides).
type IndexLimits struct {
	// Composites is the most composite indexes a database has. Default 1000.
	Composites int
	// FieldOverrides is the most field overrides a database has, its default's aside. Default
	// 1000.
	FieldOverrides int
}

// Default index limits: production's with billing (ADR-025 §2, §3).
const (
	DefaultCompositeIndexes = 1000
	DefaultFieldOverrides   = 1000
)

func (l IndexLimits) withDefaults() IndexLimits {
	l.Composites = cmp.Or(l.Composites, DefaultCompositeIndexes)
	l.FieldOverrides = cmp.Or(l.FieldOverrides, DefaultFieldOverrides)
	return l
}

// ResourceExhausted is the code of a request over a quota.
const ResourceExhausted Code = 8

// The Admin API's errors. Provisional texts.
var (
	errIndexExists       = &Error{AlreadyExists, "index already exists"}
	errIndexNotFound     = &Error{NotFound, "index not found"}
	errOperationNotFound = &Error{NotFound, "operation not found"}
	errIndexUnnecessary  = &Error{InvalidArgument, "this index is not necessary, configure using single field index controls"}
)

func invalidIndex(format string, args ...any) *Error {
	return &Error{InvalidArgument, fmt.Sprintf(format, args...)}
}

// Operation kinds, operations.kind.
const (
	opCreateIndex = "create_index"
	opUpdateField = "update_field"
)

// IndexName returns a composite index's resource name.
func (db DB) IndexName(group string, id int64) string {
	return db.Name() + "/collectionGroups/" + group + "/indexes/" + FormatIndexID(id)
}

// FieldName returns a field configuration's resource name.
func (db DB) FieldName(group, path string) string {
	return db.Name() + "/collectionGroups/" + group + "/fields/" + path
}

// OperationName returns an operation's resource name.
func (db DB) OperationName(id string) string { return db.Name() + "/operations/" + id }

// FormatIndexID writes a composite index's id as production's look (CICAgOjXh4EK): a protobuf
// varint field 1, base64 URL-safe without padding.
func FormatIndexID(id int64) string {
	return base64.RawURLEncoding.EncodeToString(protowire.AppendVarint([]byte{0x08}, uint64(id)))
}

// ParseIndexID reads FormatIndexID's form; ok is false for any other string.
func ParseIndexID(s string) (id int64, ok bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) < 2 || b[0] != 0x08 {
		return 0, false
	}
	v, n := protowire.ConsumeVarint(b[1:])
	if n < 0 || n != len(b)-1 || v == 0 || v > 1<<62 || FormatIndexID(int64(v)) != s {
		return 0, false
	}
	return int64(v), true
}

// normalizeComposite checks a composite index as the Admin API receives it (ADR-025 §3): 2 to
// 100 fields once __name__ ends them (Normalize adds it in the last field's direction), each
// path valid and once, __name__ ordered and last, at most one array-contains field and one
// vector field, and not an index every field has (one field and __name__ in its direction, at
// collection scope). It returns the index as stored.
func normalizeComposite(in *adminpb.Index) (*adminpb.Index, error) {
	scope := in.GetQueryScope()
	if scope != adminpb.Index_COLLECTION && scope != adminpb.Index_COLLECTION_GROUP {
		return nil, invalidIndex("query_scope must be COLLECTION or COLLECTION_GROUP.")
	}
	if in.GetApiScope() != adminpb.Index_ANY_API {
		return nil, invalidIndex("api_scope must be ANY_API.")
	}
	out := &adminpb.Index{QueryScope: scope}
	seen := map[string]bool{}
	contains, vectors := 0, 0
	for i, f := range in.GetFields() {
		path, err := value.ParseFieldPath(f.GetFieldPath())
		if err != nil {
			return nil, invalidIndex("%s", err.Error())
		}
		canon := path.String()
		if seen[canon] {
			return nil, invalidIndex("Field %s appears more than once in the index.", canon)
		}
		seen[canon] = true
		g := &adminpb.Index_IndexField{FieldPath: canon}
		switch {
		case f.GetVectorConfig() != nil:
			vc := f.GetVectorConfig()
			if d := vc.GetDimension(); d < 1 || d > 2048 || vc.GetFlat() == nil {
				return nil, invalidIndex("A vector field needs a dimension from 1 to 2048 and a flat index.")
			}
			vectors++
			g.ValueMode = &adminpb.Index_IndexField_VectorConfig_{VectorConfig: proto.CloneOf(vc)}
		case f.GetArrayConfig() == adminpb.Index_IndexField_CONTAINS:
			contains++
			g.ValueMode = &adminpb.Index_IndexField_ArrayConfig_{ArrayConfig: adminpb.Index_IndexField_CONTAINS}
		case f.GetOrder() == adminpb.Index_IndexField_ASCENDING || f.GetOrder() == adminpb.Index_IndexField_DESCENDING:
			g.ValueMode = &adminpb.Index_IndexField_Order_{Order: f.GetOrder()}
		default:
			return nil, invalidIndex("Field %s needs an order, an array_config or a vector_config.", canon)
		}
		if path.IsDocumentID() && (i != len(in.GetFields())-1 || g.GetOrder() == adminpb.Index_IndexField_ORDER_UNSPECIFIED) {
			return nil, invalidIndex("__name__ must be the last field of an index, ordered.")
		}
		out.Fields = append(out.Fields, g)
	}
	if contains > 1 {
		return nil, invalidIndex("An index may have at most one array_config field.")
	}
	if vectors > 1 || vectors == 1 && contains == 1 {
		return nil, invalidIndex("An index may have at most one vector_config field, and none with an array_config field.")
	}
	if n := len(out.Fields); n == 0 || out.Fields[n-1].GetFieldPath() != index.NameField {
		order := adminpb.Index_IndexField_ASCENDING
		if n > 0 && out.Fields[n-1].GetOrder() == adminpb.Index_IndexField_DESCENDING {
			order = adminpb.Index_IndexField_DESCENDING
		}
		out.Fields = append(out.Fields, &adminpb.Index_IndexField{FieldPath: index.NameField,
			ValueMode: &adminpb.Index_IndexField_Order_{Order: order}})
	}
	if n := len(out.Fields); n < 2 || n > 100 {
		return nil, invalidIndex("An index has 2 to 100 fields, __name__ included; this one has %d.", n)
	}
	if len(out.Fields) == 2 && scope == adminpb.Index_COLLECTION && vectors == 0 {
		f, name := out.Fields[0], out.Fields[1]
		if f.GetArrayConfig() == adminpb.Index_IndexField_CONTAINS && name.GetOrder() == adminpb.Index_IndexField_ASCENDING ||
			f.GetOrder() == name.GetOrder() {
			return nil, errIndexUnnecessary
		}
	}
	return out, nil
}

// newOperationID returns a fresh operation id.
func newOperationID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func marshalAny(m proto.Message) ([]byte, error) {
	a, err := anypb.New(m)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(a)
}

// CreateIndex creates a composite index of collection group group, normalized as production
// stores it, and returns its operation. The index is CREATING: from now on every commit writes
// its entries, and the build job fills it from the documents there, then makes it READY and the
// operation done (ADR-024 §2.5). An index with a vector field is stored and READY at once: it is
// never written nor served in v0.4.0 (ADR-025 §3). The same index twice is ALREADY_EXISTS, as
// firebase-tools expects of a redeploy.
func (s *PGStore) CreateIndex(ctx context.Context, db DB, group string, ix *adminpb.Index) (*longrunningpb.Operation, error) {
	if err := s.serving(); err != nil {
		return nil, err
	}
	spec, err := normalizeComposite(ix)
	if err != nil {
		return nil, err
	}
	specBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(spec)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(specBytes)
	vector := slices.ContainsFunc(spec.GetFields(), func(f *adminpb.Index_IndexField) bool { return f.GetVectorConfig() != nil })
	if _, err := s.catalog(ctx, db); err != nil { // creates '(default)' when missing
		return nil, err
	}
	opID := newOperationID()
	var op *longrunningpb.Operation
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := queries.New(tx)
		n, err := q.CountComposites(ctx, queries.CountCompositesParams{ProjectNumber: db.Project, DatabaseID: db.ID})
		if err != nil {
			return err
		}
		state := "CREATING"
		if vector {
			state = "READY"
		}
		rows, err := q.InsertComposite(ctx, queries.InsertCompositeParams{ProjectNumber: db.Project, DatabaseID: db.ID,
			CollectionGroup: group, QueryScope: int16(spec.GetQueryScope()), Spec: specBytes, SpecHash: hash[:],
			State: state, OperationID: &opID})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return errIndexExists
		}
		if n >= int64(s.limits.Composites) {
			return &Error{ResourceExhausted, fmt.Sprintf("The database has the most composite indexes allowed (%d).", s.limits.Composites)}
		}
		id := rows[0].ID
		md := &adminpb.IndexOperationMetadata{StartTime: timestamppb.New(rows[0].CreateTime), Index: db.IndexName(group, id),
			State: adminpb.OperationState_PROCESSING}
		out := proto.CloneOf(spec)
		out.Name, out.State = db.IndexName(group, id), adminpb.Index_CREATING
		var result []byte
		if vector {
			md.State, md.EndTime = adminpb.OperationState_SUCCESSFUL, md.StartTime
			out.State = adminpb.Index_READY
			if result, err = marshalAny(out); err != nil {
				return err
			}
		}
		mdBytes, err := marshalAny(md)
		if err != nil {
			return err
		}
		if err := q.InsertOperation(ctx, queries.InsertOperationParams{ProjectNumber: db.Project, DatabaseID: db.ID,
			ID: opID, Kind: opCreateIndex, Metadata: mdBytes, Done: vector, Result: result}); err != nil {
			return err
		}
		if !vector {
			if err := q.BumpCatalog(ctx, queries.BumpCatalogParams{ProjectNumber: db.Project, DatabaseID: db.ID}); err != nil {
				return err
			}
		}
		op, err = operationOf(db, opID, mdBytes, vector, result)
		return err
	})
	s.cats.drop(db)
	if err != nil {
		return nil, err
	}
	s.wakeBuilds()
	return op, nil
}

// operationOf builds an operation from its row.
func operationOf(db DB, id string, metadata []byte, done bool, result []byte) (*longrunningpb.Operation, error) {
	op := &longrunningpb.Operation{Name: db.OperationName(id), Done: done, Metadata: &anypb.Any{}}
	if err := proto.Unmarshal(metadata, op.Metadata); err != nil {
		return nil, err
	}
	if done && result != nil {
		a := &anypb.Any{}
		if err := proto.Unmarshal(result, a); err != nil {
			return nil, err
		}
		op.Result = &longrunningpb.Operation_Response{Response: a}
	}
	return op, nil
}

// indexProto is a composite index as the Admin API shows it.
func (db DB) indexProto(c *composite) *adminpb.Index {
	out := proto.CloneOf(c.spec)
	out.Name, out.State = db.IndexName(c.group, c.id), adminpb.Index_CREATING
	if c.ready {
		out.State = adminpb.Index_READY
	}
	return out
}

// ListIndexes returns db's composite indexes of collection group group, or of every group for
// "" or "-", by id: CREATING and READY ones, not those being deleted.
func (s *PGStore) ListIndexes(ctx context.Context, db DB, group string) ([]*adminpb.Index, error) {
	if err := s.serving(); err != nil {
		return nil, err
	}
	cat, err := s.catalog(ctx, db)
	if err != nil {
		return nil, err
	}
	out := []*adminpb.Index{}
	for _, c := range cat.composites {
		if group == "" || group == "-" || c.group == group {
			out = append(out, db.indexProto(c))
		}
	}
	return out, nil
}

// GetIndex returns one composite index by its group and id (FormatIndexID's form).
func (s *PGStore) GetIndex(ctx context.Context, db DB, group, id string) (*adminpb.Index, error) {
	if err := s.serving(); err != nil {
		return nil, err
	}
	n, ok := ParseIndexID(id)
	if !ok {
		return nil, errIndexNotFound
	}
	cat, err := s.catalog(ctx, db)
	if err != nil {
		return nil, err
	}
	for _, c := range cat.composites {
		if c.id == n && c.group == group {
			return db.indexProto(c), nil
		}
	}
	return nil, errIndexNotFound
}

// DeleteIndex deletes a composite index: it leaves every commit and the planner at once, a
// CREATING one's operation ends CANCELLED, and the build job purges its entries in batches
// (ADR-024 §2.5).
func (s *PGStore) DeleteIndex(ctx context.Context, db DB, group, id string) error {
	if err := s.serving(); err != nil {
		return err
	}
	n, ok := ParseIndexID(id)
	if !ok {
		return errIndexNotFound
	}
	if _, err := s.catalog(ctx, db); err != nil {
		return err
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := queries.New(tx)
		opID, err := q.MarkCompositeDeleting(ctx, queries.MarkCompositeDeletingParams{ProjectNumber: db.Project,
			DatabaseID: db.ID, ID: n, CollectionGroup: group})
		if errors.Is(err, pgx.ErrNoRows) {
			return errIndexNotFound
		}
		if err != nil {
			return err
		}
		if err := q.BumpCatalog(ctx, queries.BumpCatalogParams{ProjectNumber: db.Project, DatabaseID: db.ID}); err != nil {
			return err
		}
		if opID == nil {
			return nil
		}
		return finishOperation(ctx, q, db, *opID, adminpb.OperationState_CANCELLED, nil)
	})
	s.cats.drop(db)
	if err != nil {
		return err
	}
	s.wakeBuilds()
	return nil
}

// finishOperation ends an operation that is not done yet: its metadata's state and end time,
// and its response, if any. A done operation is left as it is.
func finishOperation(ctx context.Context, q *queries.Queries, db DB, id string, state adminpb.OperationState, response proto.Message) error {
	row, err := q.LockOperation(ctx, queries.LockOperationParams{ProjectNumber: db.Project, DatabaseID: db.ID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil || row.Done {
		return err
	}
	a := &anypb.Any{}
	if err := proto.Unmarshal(row.Metadata, a); err != nil {
		return err
	}
	md, err := a.UnmarshalNew()
	if err != nil {
		return err
	}
	now := timestamppb.Now()
	switch m := md.(type) {
	case *adminpb.IndexOperationMetadata:
		m.State, m.EndTime = state, now
	case *adminpb.FieldOperationMetadata:
		m.State, m.EndTime = state, now
	}
	mdBytes, err := marshalAny(md)
	if err != nil {
		return err
	}
	var result []byte
	if response != nil {
		if result, err = marshalAny(response); err != nil {
			return err
		}
	}
	return q.UpdateOperation(ctx, queries.UpdateOperationParams{ProjectNumber: db.Project, DatabaseID: db.ID, ID: id,
		Metadata: mdBytes, Done: true, Result: result})
}

// GetOperation returns an operation of db.
func (s *PGStore) GetOperation(ctx context.Context, db DB, id string) (*longrunningpb.Operation, error) {
	if err := s.serving(); err != nil {
		return nil, err
	}
	row, err := s.q.GetOperation(ctx, queries.GetOperationParams{ProjectNumber: db.Project, DatabaseID: db.ID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errOperationNotFound
	}
	if err != nil {
		return nil, err
	}
	return operationOf(db, id, row.Metadata, row.Done, row.Result)
}

// ListOperations returns db's operations, oldest first (done ones are kept for
// RetentionOptions.Operations).
func (s *PGStore) ListOperations(ctx context.Context, db DB) ([]*longrunningpb.Operation, error) {
	if err := s.serving(); err != nil {
		return nil, err
	}
	rows, err := s.q.ListOperations(ctx, queries.ListOperationsParams{ProjectNumber: db.Project, DatabaseID: db.ID})
	if err != nil {
		return nil, err
	}
	out := make([]*longrunningpb.Operation, 0, len(rows))
	for _, r := range rows {
		op, err := operationOf(db, r.ID, r.Metadata, r.Done, r.Result)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, nil
}

// canonicalField checks a field configuration's name: a field path, or index.AllFields; the
// database default is __default__ and * alone. It returns the path in canonical form.
func canonicalField(group, path string) (string, error) {
	if path == index.AllFields {
		return path, nil
	}
	if group == index.DefaultCollectionGroup {
		return "", invalidIndex("The collection group __default__ has the field * alone.")
	}
	p, err := value.ParseFieldPath(path)
	if err != nil {
		return "", invalidIndex("%s", err.Error())
	}
	if p.IsDocumentID() {
		return "", invalidIndex("__name__ has no single-field index configuration.")
	}
	return p.String(), nil
}

// fieldIndexProtos writes a configuration's indexes as a Field's index_config holds them.
func fieldIndexProtos(path string, ixs []index.SingleFieldIndex) []*adminpb.Index {
	out := make([]*adminpb.Index, 0, len(ixs))
	for _, ix := range ixs {
		f := &adminpb.Index_IndexField{FieldPath: path}
		switch ix.Mode {
		case index.Contains:
			f.ValueMode = &adminpb.Index_IndexField_ArrayConfig_{ArrayConfig: adminpb.Index_IndexField_CONTAINS}
		case index.Descending:
			f.ValueMode = &adminpb.Index_IndexField_Order_{Order: adminpb.Index_IndexField_DESCENDING}
		default:
			f.ValueMode = &adminpb.Index_IndexField_Order_{Order: adminpb.Index_IndexField_ASCENDING}
		}
		state := adminpb.Index_READY
		if ix.State == index.StateCreating {
			state = adminpb.Index_CREATING
		}
		out = append(out, &adminpb.Index{QueryScope: adminpb.Index_QueryScope(ix.Scope), Fields: []*adminpb.Index_IndexField{f}, State: state})
	}
	return out
}

// fieldProto is the Field resource of group's path under cat: its own configuration, or the one
// it inherits (usesAncestorConfig) and from which field (ancestorField), and its TTL policy.
func (db DB) fieldProto(cat *catalog, group, path string) *adminpb.Field {
	f := &adminpb.Field{Name: db.FieldName(group, path), IndexConfig: &adminpb.Field_IndexConfig{}}
	var own *overrideRow
	for i := range cat.overrides {
		if r := &cat.overrides[i]; r.group == group && r.path == path {
			own = r
		}
	}
	if own != nil && own.ttlState != "" {
		f.TtlConfig = &adminpb.Field_TtlConfig{State: adminpb.Field_TtlConfig_State(adminpb.Field_TtlConfig_State_value[own.ttlState])}
	}
	if ixs, ok := cat.fields[overrideKey{group, path}]; ok {
		f.IndexConfig.Indexes = fieldIndexProtos(path, ixs)
		if own != nil && own.reverting {
			f.IndexConfig.Reverting = true
			f.IndexConfig.AncestorField = db.FieldName(ancestorOf(cat.fields, group, path))
		}
		return f
	}
	ag, ap := ancestorOf(cat.fields, group, path)
	f.IndexConfig.Indexes = fieldIndexProtos(path, cat.fields.effective(group, path))
	if group != index.DefaultCollectionGroup {
		f.IndexConfig.UsesAncestorConfig, f.IndexConfig.AncestorField = true, db.FieldName(ag, ap)
	}
	return f
}

// ancestorOf names the configuration path inherits: its nearest ancestor's, its group's *, or
// the database default (which the automatic indexes stand for when it has no row).
func ancestorOf(c fieldConfigs, group, path string) (string, string) {
	if path != index.AllFields {
		if fp, err := value.ParseFieldPath(path); err == nil {
			for i := len(fp) - 1; i > 0; i-- {
				if a := fp[:i].String(); hasConfig(c, group, a) {
					return group, a
				}
			}
		}
		if hasConfig(c, group, index.AllFields) {
			return group, index.AllFields
		}
	}
	return index.DefaultCollectionGroup, index.AllFields
}

func hasConfig(c fieldConfigs, group, path string) bool {
	_, ok := c[overrideKey{group, path}]
	return ok
}

// GetField returns the single-field configuration of a field (or * or the database default).
func (s *PGStore) GetField(ctx context.Context, db DB, group, path string) (*adminpb.Field, error) {
	if err := s.serving(); err != nil {
		return nil, err
	}
	path, err := canonicalField(group, path)
	if err != nil {
		return nil, err
	}
	cat, err := s.catalog(ctx, db)
	if err != nil {
		return nil, err
	}
	return db.fieldProto(cat, group, path), nil
}

// ListFields returns the fields of collection group group, or of every group for "" or "-",
// with an explicit configuration (index_config or ttl_config), and the database default
// __default__/fields/* (as production lists it, which firebase-tools drops), ordered by group
// and path. A filter such as firebase-tools' indexConfig.usesAncestorConfig=false is the
// transport's to apply.
func (s *PGStore) ListFields(ctx context.Context, db DB, group string) ([]*adminpb.Field, error) {
	if err := s.serving(); err != nil {
		return nil, err
	}
	cat, err := s.catalog(ctx, db)
	if err != nil {
		return nil, err
	}
	all := group == "" || group == "-"
	type key struct{ group, path string }
	var keys []key
	if all || group == index.DefaultCollectionGroup {
		keys = append(keys, key{index.DefaultCollectionGroup, index.AllFields})
	}
	for _, r := range cat.overrides {
		if (all || r.group == group) && !(r.group == index.DefaultCollectionGroup && r.path == index.AllFields) {
			keys = append(keys, key{r.group, r.path})
		}
	}
	slices.SortFunc(keys, func(a, b key) int { return cmp.Or(strings.Compare(a.group, b.group), strings.Compare(a.path, b.path)) })
	out := make([]*adminpb.Field, len(keys))
	for i, k := range keys {
		out[i] = db.fieldProto(cat, k.group, k.path)
	}
	return out, nil
}

// UpdateField changes a field's single-field configuration and TTL policy (PATCH …/fields/
// {path}, with update_mask index_config, ttl_config or both, both when empty): an index_config
// lists the field's indexes (none: exempt), its absence returns the field to its ancestor's
// configuration; a ttl_config sets a TTL policy, its absence removes it. The change is queued
// and its operation returned: the build job applies the database's field updates one at a time,
// each as an index is built (indexbuild.go).
func (s *PGStore) UpdateField(ctx context.Context, db DB, group string, f *adminpb.Field, mask []string) (*longrunningpb.Operation, error) {
	if err := s.serving(); err != nil {
		return nil, err
	}
	name := f.GetName()
	prefix := db.Name() + "/collectionGroups/" + group + "/fields/"
	raw, ok := strings.CutPrefix(name, prefix)
	if !ok {
		return nil, invalidIndex("The field's name must be under %s.", prefix)
	}
	path, err := canonicalField(group, raw) // a path may come quoted: `a.b`
	if err != nil {
		return nil, err
	}
	setIndex, setTTL, err := fieldMask(mask)
	if err != nil {
		return nil, err
	}
	if setIndex && f.GetIndexConfig() != nil {
		if _, err := overrideIndexes(f.GetIndexConfig()); err != nil {
			return nil, err
		}
	}
	if setTTL && f.GetTtlConfig() != nil && (path == index.AllFields || group == index.DefaultCollectionGroup) {
		return nil, invalidIndex("A TTL policy names one field of one collection group.")
	}
	cat, err := s.catalog(ctx, db)
	if err != nil {
		return nil, err
	}
	_, explicit := cat.fields[overrideKey{group, path}]
	for _, r := range cat.overrides {
		explicit = explicit || r.group == group && r.path == path && r.ttlState != ""
	}
	grows := !explicit && group != index.DefaultCollectionGroup &&
		(setIndex && f.GetIndexConfig() != nil || setTTL && f.GetTtlConfig() != nil)
	field := &adminpb.Field{Name: db.FieldName(group, path)}
	var paths []string
	if setIndex {
		field.IndexConfig = proto.CloneOf(f.GetIndexConfig())
		paths = append(paths, "index_config")
	}
	if setTTL {
		field.TtlConfig = proto.CloneOf(f.GetTtlConfig())
		paths = append(paths, "ttl_config")
	}
	req, err := proto.Marshal(&adminpb.UpdateFieldRequest{Field: field, UpdateMask: &fieldmaskpb.FieldMask{Paths: paths}})
	if err != nil {
		return nil, err
	}
	opID := newOperationID()
	var op *longrunningpb.Operation
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := queries.New(tx)
		if grows {
			n, err := q.CountFieldOverrides(ctx, queries.CountFieldOverridesParams{ProjectNumber: db.Project, DatabaseID: db.ID})
			if err != nil {
				return err
			}
			if n >= int64(s.limits.FieldOverrides) {
				return &Error{ResourceExhausted, fmt.Sprintf("The database has the most field overrides allowed (%d).", s.limits.FieldOverrides)}
			}
		}
		md := &adminpb.FieldOperationMetadata{StartTime: timestamppb.Now(), Field: field.GetName(),
			State: adminpb.OperationState_INITIALIZING}
		mdBytes, err := marshalAny(md)
		if err != nil {
			return err
		}
		if err := q.InsertOperation(ctx, queries.InsertOperationParams{ProjectNumber: db.Project, DatabaseID: db.ID,
			ID: opID, Kind: opUpdateField, Metadata: mdBytes, Request: req}); err != nil {
			return err
		}
		op, err = operationOf(db, opID, mdBytes, false, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.wakeBuilds()
	return op, nil
}

// fieldMask reads an UpdateField mask: empty updates both parts.
func fieldMask(mask []string) (index, ttl bool, err error) {
	if len(mask) == 0 {
		return true, true, nil
	}
	for _, m := range mask {
		switch m {
		case "index_config", "indexConfig":
			index = true
		case "ttl_config", "ttlConfig":
			ttl = true
		default:
			return false, false, invalidIndex("The update mask may name index_config and ttl_config only, not %s.", m)
		}
	}
	return index, ttl, nil
}

// overrideIndexes checks an index_config's indexes: each of one field, ordered or
// array-contains, at collection or collection-group scope, and each once.
func overrideIndexes(c *adminpb.Field_IndexConfig) ([]index.SingleFieldIndex, error) {
	var out []index.SingleFieldIndex
	for _, ix := range c.GetIndexes() {
		sf, err := singleFieldOf(ix)
		if err != nil {
			return nil, invalidIndex("%s.", strings.ToUpper(err.Error()[:1])+err.Error()[1:])
		}
		if slices.Contains(out, sf) {
			return nil, invalidIndex("An index appears more than once in the field's configuration.")
		}
		out = append(out, sf)
	}
	return out, nil
}
