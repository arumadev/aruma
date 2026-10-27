package core

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/queries"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
)

// The index build job (ADR-024 §2.5): it fills a CREATING composite index from the documents
// already there and makes it READY, purges a deleted one's entries, and applies the queued field
// updates, one at a time per database, rebuilding the single-field entries they change.
//
// A composite is filled while commits write it (from the catalog version that created it, every
// commit writes its entries): the documents of its collection group in name-key batches, each a
// short transaction that locks its documents FOR SHARE, so that a commit of one of them comes
// wholly before or after the batch, and inserts their entries, which a commit may have written
// already. The last name key of each batch is stored with the index: a build resumes there
// after a restart. Only an empty batch ends the build: READY, the catalog's next version, and
// the operation done, in one transaction.
//
// A field update changes what the single-field indexes of a field (and its subfields, or every
// field of a group, or of the database) are. It applies the new configuration at a new catalog
// version, with the indexes it adds CREATING; from there every commit leaves a document with the
// new configuration's entries and deletes the old one's too (plan.indexArgs). The documents the
// change may touch are then rebuilt in batches as a composite is filled: each one's entries
// under the old configuration that the new one lacks are deleted, the new ones inserted. The
// last batch makes the added indexes READY (or removes a reverted override) at the next version.

// IndexBuildOptions times the build job; the zero value is the defaults.
type IndexBuildOptions struct {
	// Every is how often the job looks for work it was not told of. Default 1 second.
	Every time.Duration
	// Batch is the documents of one build batch. Default 500.
	Batch int
	// PurgeBatch is the entries one purge statement deletes. Default 5,000.
	PurgeBatch int
	Log        *slog.Logger

	// maxBatches, set by tests, ends a pass after that many build batches, as a restart would.
	maxBatches int
}

func (o IndexBuildOptions) withDefaults() IndexBuildOptions {
	o.Every = cmp.Or(o.Every, time.Second)
	o.Batch = cmp.Or(o.Batch, 500)
	o.PurgeBatch = cmp.Or(o.PurgeBatch, 5000)
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return o
}

// errBuildStopped ends a pass that reached IndexBuildOptions.maxBatches.
var errBuildStopped = errors.New("documents: index build stopped")

// wakeBuilds tells the build job there is work.
func (s *PGStore) wakeBuilds() {
	select {
	case s.builds <- struct{}{}:
	default:
	}
}

// RunIndexBuilds runs the build job until ctx ends: a pass when told of work (CreateIndex,
// DeleteIndex, UpdateField) and every o.Every, while this process holds the single-instance
// lock. A pass that fails is logged and tried again.
func (s *PGStore) RunIndexBuilds(ctx context.Context, o IndexBuildOptions) {
	o = o.withDefaults()
	t := time.NewTicker(o.Every)
	defer t.Stop()
	for {
		if s.serving() == nil {
			if err := s.BuildIndexes(ctx, o); err != nil && ctx.Err() == nil && !errors.Is(err, ErrNotServing) {
				o.Log.Error("documents: index build", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.builds:
		}
	}
}

// BuildIndexes runs every pending build of every database to its end: deleted composites
// purged, CREATING ones filled, field updates applied. It returns when none is left, or with
// ErrNotServing when the process loses the lock.
func (s *PGStore) BuildIndexes(ctx context.Context, o IndexBuildOptions) error {
	o = o.withDefaults()
	batches := 0
	for {
		if err := s.serving(); err != nil {
			return err
		}
		ixs, err := s.q.PendingIndexBuilds(ctx)
		if err != nil {
			return err
		}
		fields, err := s.q.PendingFieldBuilds(ctx)
		if err != nil {
			return err
		}
		if len(ixs) == 0 && len(fields) == 0 {
			return nil
		}
		for _, r := range ixs {
			db := DB{Project: r.ProjectNumber, ID: r.DatabaseID}
			if r.State == "DELETING" {
				err = s.purgeComposite(ctx, db, r.ID, o)
			} else {
				err = s.fillComposite(ctx, db, r.ID, o, &batches)
			}
			if err != nil {
				return err
			}
		}
		for _, r := range fields {
			db := DB{Project: r.ProjectNumber, ID: r.DatabaseID}
			if r.OperationID != "" {
				err = s.startFieldUpdate(ctx, db, r.OperationID)
			} else {
				err = s.rebuildField(ctx, db, r.KeyHash, o, &batches)
			}
			if err != nil {
				return err
			}
		}
	}
}

// step counts a build batch, and stops the pass at o.maxBatches.
func step(o IndexBuildOptions, batches *int) error {
	if *batches++; o.maxBatches > 0 && *batches > o.maxBatches {
		return errBuildStopped
	}
	return nil
}

// batchDocs reads a batch of documents to build, locked FOR SHARE: the collection group's, or
// every document's for the database default.
func batchDocs(ctx context.Context, q *queries.Queries, db DB, group string, after []byte, n int) ([]queries.BackfillAllRow, error) {
	if after == nil {
		after = []byte{}
	}
	if group == index.DefaultCollectionGroup {
		return q.BackfillAll(ctx, queries.BackfillAllParams{ProjectNumber: db.Project, DatabaseID: db.ID, After: after, Batch: int32(n)})
	}
	hash := sha256.Sum256([]byte(group))
	rows, err := q.BackfillGroup(ctx, queries.BackfillGroupParams{ProjectNumber: db.Project, DatabaseID: db.ID,
		CgHash: hash[:], CollectionID: group, After: after, Batch: int32(n)})
	out := make([]queries.BackfillAllRow, len(rows))
	for i, r := range rows {
		out[i] = queries.BackfillAllRow(r)
	}
	return out, err
}

// insertEntries and deleteEntries write a build batch's entries.
func insertEntries(ctx context.Context, q *queries.Queries, db DB, es []entry) error {
	if len(es) == 0 {
		return nil
	}
	p := queries.InsertIndexEntriesParams{ProjectNumber: db.Project, DatabaseID: db.ID}
	for _, e := range sortedEntries(es) {
		key, full := storedEntry([]byte(e.key))
		p.IndexIds, p.Keys, p.FullKeys = append(p.IndexIds, e.index), append(p.Keys, key), append(p.FullKeys, full)
	}
	return q.InsertIndexEntries(ctx, p)
}

func deleteEntries(ctx context.Context, q *queries.Queries, db DB, es []entry) error {
	if len(es) == 0 {
		return nil
	}
	p := queries.DeleteIndexEntriesParams{ProjectNumber: db.Project, DatabaseID: db.ID}
	for _, e := range sortedEntries(es) {
		key, _ := storedEntry([]byte(e.key))
		p.IndexIds, p.Keys = append(p.IndexIds, e.index), append(p.Keys, key)
	}
	return q.DeleteIndexEntries(ctx, p)
}

// fillComposite fills a CREATING composite index batch by batch, then makes it READY.
func (s *PGStore) fillComposite(ctx context.Context, db DB, id int64, o IndexBuildOptions, batches *int) error {
	for {
		if err := s.serving(); err != nil {
			return err
		}
		if err := step(o, batches); err != nil {
			return err
		}
		done := false
		err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			q := queries.New(tx)
			row, err := q.LockCompositeBuild(ctx, queries.LockCompositeBuildParams{ProjectNumber: db.Project, DatabaseID: db.ID, ID: id})
			if errors.Is(err, pgx.ErrNoRows) || err == nil && row.State != "CREATING" {
				done = true
				return nil
			}
			if err != nil {
				return err
			}
			ix, err := decodeComposite(id, row.CollectionGroup, row.QueryScope, row.Spec, row.State)
			if err != nil {
				return err
			}
			docs, err := batchDocs(ctx, q, db, ix.group, row.BuildCursor, o.Batch)
			if err != nil {
				return err
			}
			if len(docs) == 0 {
				done = true
				return s.compositeReady(ctx, q, db, ix, row.OperationID)
			}
			var es []entry
			for _, d := range docs {
				p, err := pathOfKey(d.Name)
				if err != nil {
					return err
				}
				fields, _, _, err := decodeDocument(d.Doc)
				if err != nil {
					return err
				}
				de := newDocEntries(p)
				de.composite(ix, fields)
				for e := range de.set {
					es = append(es, e)
				}
			}
			if err := insertEntries(ctx, q, db, es); err != nil {
				return err
			}
			return q.SetCompositeCursor(ctx, queries.SetCompositeCursorParams{ProjectNumber: db.Project, DatabaseID: db.ID,
				ID: id, BuildCursor: docs[len(docs)-1].NameKey})
		})
		if err != nil {
			return fmt.Errorf("documents: fill index %d of %d/%s: %w", id, db.Project, db.ID, err)
		}
		if done {
			s.cats.drop(db)
			return nil
		}
	}
}

// compositeReady ends a composite's build: READY, the catalog's next version, the operation done.
func (s *PGStore) compositeReady(ctx context.Context, q *queries.Queries, db DB, ix *composite, opID *string) error {
	if _, err := q.MarkCompositeReady(ctx, queries.MarkCompositeReadyParams{ProjectNumber: db.Project, DatabaseID: db.ID, ID: ix.id}); err != nil {
		return err
	}
	if err := q.BumpCatalog(ctx, queries.BumpCatalogParams{ProjectNumber: db.Project, DatabaseID: db.ID}); err != nil {
		return err
	}
	if opID == nil {
		return nil
	}
	// The operation names the index by its project id, which the row does not hold: it is in
	// the metadata the operation was created with.
	row, err := q.LockOperation(ctx, queries.LockOperationParams{ProjectNumber: db.Project, DatabaseID: db.ID, ID: *opID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	md, err := unmarshalMetadata(row.Metadata)
	if err != nil {
		return err
	}
	out := proto.CloneOf(ix.spec)
	if m, ok := md.(*adminpb.IndexOperationMetadata); ok {
		out.Name = m.GetIndex()
	}
	out.State = adminpb.Index_READY
	return finishOperation(ctx, q, db, *opID, adminpb.OperationState_SUCCESSFUL, out)
}

func unmarshalMetadata(b []byte) (proto.Message, error) {
	op, err := operationOf(DB{}, "", b, false, nil)
	if err != nil {
		return nil, err
	}
	return op.GetMetadata().UnmarshalNew()
}

// purgeComposite deletes a deleted composite's entries in batches, then its row.
func (s *PGStore) purgeComposite(ctx context.Context, db DB, id int64, o IndexBuildOptions) error {
	for {
		if err := s.serving(); err != nil {
			return err
		}
		n, err := deleteBatch(ctx, func(ctx context.Context) (int64, error) {
			return s.q.PurgeCompositeEntries(ctx, queries.PurgeCompositeEntriesParams{ProjectNumber: db.Project,
				DatabaseID: db.ID, IndexID: id, Batch: int32(o.PurgeBatch)})
		})
		if err != nil {
			return fmt.Errorf("documents: purge index %d of %d/%s: %w", id, db.Project, db.ID, err)
		}
		if n < int64(o.PurgeBatch) {
			break
		}
	}
	return s.q.DeleteComposite(ctx, queries.DeleteCompositeParams{ProjectNumber: db.Project, DatabaseID: db.ID, ID: id})
}

// fieldKeyHash is field_overrides.key_hash: SHA-256 of the group, 0x00 and the path.
func fieldKeyHash(group, path string) []byte {
	h := sha256.Sum256([]byte(group + "\x00" + path))
	return h[:]
}

// splitFieldName reads a field configuration's name.
func splitFieldName(db DB, name string) (group, path string, ok bool) {
	rest, ok := strings.CutPrefix(name, db.Name()+"/collectionGroups/")
	if !ok {
		return "", "", false
	}
	return strings.Cut(rest, "/fields/")
}

// sameIndexes reports whether two configurations have the same indexes, whatever their states.
func sameIndexes(a, b []index.SingleFieldIndex) bool {
	key := func(ixs []index.SingleFieldIndex) []index.SingleFieldIndex {
		out := make([]index.SingleFieldIndex, len(ixs))
		for i, ix := range ixs {
			out[i] = index.SingleFieldIndex{Scope: ix.Scope, Mode: ix.Mode}
		}
		slices.SortFunc(out, func(x, y index.SingleFieldIndex) int {
			return cmp.Or(cmp.Compare(x.Scope, y.Scope), cmp.Compare(x.Mode, y.Mode))
		})
		return out
	}
	return slices.Equal(key(a), key(b))
}

func hasIndex(ixs []index.SingleFieldIndex, ix index.SingleFieldIndex) bool {
	return slices.ContainsFunc(ixs, func(x index.SingleFieldIndex) bool { return x.Scope == ix.Scope && x.Mode == ix.Mode })
}

// fieldUpdate is a field configuration's row as a field update leaves it.
type fieldUpdate struct {
	group, path string
	keyHash     []byte
	config      []index.SingleFieldIndex // nil: inherits
	ttlState    string
	building    bool
	prior       []byte
	reverting   bool
	opID        string
}

// marshalIndexConfig encodes a configuration as field_overrides.index_config holds it.
func marshalIndexConfig(path string, ixs []index.SingleFieldIndex) ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(&adminpb.Field_IndexConfig{Indexes: fieldIndexProtos(path, ixs)})
}

// write stores the row, or deletes it when it holds nothing.
func (u *fieldUpdate) write(ctx context.Context, q *queries.Queries, db DB) error {
	if u.config == nil && u.ttlState == "" && !u.building {
		return q.DeleteFieldOverride(ctx, queries.DeleteFieldOverrideParams{ProjectNumber: db.Project, DatabaseID: db.ID, KeyHash: u.keyHash})
	}
	var cfg []byte
	if u.config != nil {
		var err error
		if cfg, err = marshalIndexConfig(u.path, u.config); err != nil {
			return err
		}
	}
	p := queries.UpsertFieldOverrideParams{ProjectNumber: db.Project, DatabaseID: db.ID, KeyHash: u.keyHash,
		CollectionGroup: u.group, FieldPath: u.path, IndexConfig: cfg, Etag: newOperationID(), Building: u.building,
		PriorIndexConfig: u.prior, Reverting: u.reverting}
	if u.ttlState != "" {
		p.TtlState = &u.ttlState
	}
	if u.opID != "" {
		p.OperationID = &u.opID
	}
	return q.UpsertFieldOverride(ctx, p)
}

// response is the Field the update leaves, for its operation: fields is the configuration
// with the update applied.
func (u *fieldUpdate) response(db DB, cat *catalog, fields fieldConfigs) *adminpb.Field {
	after := &catalog{fields: fields}
	for _, r := range cat.overrides {
		if r.group != u.group || r.path != u.path {
			after.overrides = append(after.overrides, r)
		}
	}
	after.overrides = append(after.overrides, overrideRow{group: u.group, path: u.path, ttlState: u.ttlState, reverting: u.reverting})
	return db.fieldProto(after, u.group, u.path)
}

// startFieldUpdate applies a queued field update: at once when the field's indexes stay the
// same (a TTL policy, or a configuration made explicit), else as a build at a new catalog
// version, with the indexes it adds CREATING.
func (s *PGStore) startFieldUpdate(ctx context.Context, db DB, opID string) error {
	s.cats.drop(db)
	cat, err := s.cats.get(ctx, db)
	if err != nil {
		return err
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		q := queries.New(tx)
		op, err := q.LockOperation(ctx, queries.LockOperationParams{ProjectNumber: db.Project, DatabaseID: db.ID, ID: opID})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil || op.Done || op.Request == nil {
			return err
		}
		req := &adminpb.UpdateFieldRequest{}
		if err := proto.Unmarshal(op.Request, req); err != nil {
			return err
		}
		db := db
		// The operation's database is named by its project id, which only the field name holds.
		project, _, _ := strings.Cut(strings.TrimPrefix(req.GetField().GetName(), "projects/"), "/")
		db.ProjectID = project
		group, path, ok := splitFieldName(db, req.GetField().GetName())
		if !ok {
			return fmt.Errorf("documents: operation %s: malformed field name %q", opID, req.GetField().GetName())
		}
		u := &fieldUpdate{group: group, path: path, keyHash: fieldKeyHash(group, path)}
		k := overrideKey{group, path}
		own, explicit := cat.fields[k]
		for _, r := range cat.overrides {
			if r.group == group && r.path == path {
				u.ttlState = r.ttlState
			}
		}
		if explicit {
			u.config = own
		}
		setIndex, setTTL := false, false
		for _, m := range req.GetUpdateMask().GetPaths() {
			setIndex = setIndex || m == "index_config"
			setTTL = setTTL || m == "ttl_config"
		}
		if setIndex {
			u.config = nil
			if c := req.GetField().GetIndexConfig(); c != nil {
				if u.config, err = overrideIndexes(c); err != nil {
					return err
				}
				if u.config == nil {
					u.config = []index.SingleFieldIndex{}
				}
			}
		}
		if setTTL {
			u.ttlState = ""
			if req.GetField().GetTtlConfig() != nil {
				u.ttlState = "CREATING"
			}
		}
		old := cat.fields.effective(group, path)
		without := maps.Clone(cat.fields)
		delete(without, k)
		target := u.config
		if target == nil {
			target = without.effective(group, path)
		}
		md := &adminpb.FieldOperationMetadata{}
		if m, err := unmarshalMetadata(op.Metadata); err == nil {
			if fm, ok := m.(*adminpb.FieldOperationMetadata); ok {
				md = fm
			}
		}
		for _, ix := range target {
			if !hasIndex(old, ix) {
				md.IndexConfigDeltas = append(md.IndexConfigDeltas, &adminpb.FieldOperationMetadata_IndexConfigDelta{
					ChangeType: adminpb.FieldOperationMetadata_IndexConfigDelta_ADD, Index: fieldIndexProtos(path, []index.SingleFieldIndex{ix})[0]})
			}
		}
		for _, ix := range old {
			if !hasIndex(target, ix) {
				md.IndexConfigDeltas = append(md.IndexConfigDeltas, &adminpb.FieldOperationMetadata_IndexConfigDelta{
					ChangeType: adminpb.FieldOperationMetadata_IndexConfigDelta_REMOVE, Index: fieldIndexProtos(path, []index.SingleFieldIndex{ix})[0]})
			}
		}
		if setTTL {
			change := adminpb.FieldOperationMetadata_TtlConfigDelta_REMOVE
			if u.ttlState != "" {
				change = adminpb.FieldOperationMetadata_TtlConfigDelta_ADD
			}
			md.TtlConfigDelta = &adminpb.FieldOperationMetadata_TtlConfigDelta{ChangeType: change}
		}
		if sameIndexes(target, old) {
			// Nothing to build: the new configuration and the TTL policy apply at once. TTL
			// policies are stored, not yet enforced (ADR-028), so ACTIVE at once.
			if u.ttlState == "CREATING" {
				u.ttlState = "ACTIVE"
			}
			if err := u.write(ctx, q, db); err != nil {
				return err
			}
			if err := q.BumpCatalog(ctx, queries.BumpCatalogParams{ProjectNumber: db.Project, DatabaseID: db.ID}); err != nil {
				return err
			}
			fields := without
			if u.config != nil {
				fields = maps.Clone(without)
				fields[k] = u.config
			}
			if err := saveMetadata(ctx, q, db, opID, op, md); err != nil {
				return err
			}
			return finishOperation(ctx, q, db, opID, adminpb.OperationState_SUCCESSFUL, u.response(db, cat, fields))
		}
		building := make([]index.SingleFieldIndex, len(target))
		for i, ix := range target {
			building[i] = index.SingleFieldIndex{Scope: ix.Scope, Mode: ix.Mode}
			if !hasIndex(old, ix) {
				building[i].State = index.StateCreating
			}
		}
		u.reverting = u.config == nil
		u.config, u.building, u.opID = building, true, opID
		if explicit {
			if u.prior, err = marshalIndexConfig(path, own); err != nil {
				return err
			}
		}
		if err := u.write(ctx, q, db); err != nil {
			return err
		}
		if err := q.BumpCatalog(ctx, queries.BumpCatalogParams{ProjectNumber: db.Project, DatabaseID: db.ID}); err != nil {
			return err
		}
		md.State = adminpb.OperationState_PROCESSING
		return saveMetadata(ctx, q, db, opID, op, md)
	})
	s.cats.drop(db)
	if err != nil {
		return fmt.Errorf("documents: start field update %s of %d/%s: %w", opID, db.Project, db.ID, err)
	}
	return nil
}

// saveMetadata stores an operation's metadata and takes its request off the queue.
func saveMetadata(ctx context.Context, q *queries.Queries, db DB, id string, op queries.LockOperationRow, md proto.Message) error {
	b, err := marshalAny(md)
	if err != nil {
		return err
	}
	return q.UpdateOperation(ctx, queries.UpdateOperationParams{ProjectNumber: db.Project, DatabaseID: db.ID, ID: id,
		Metadata: b, Done: op.Done, Result: op.Result})
}

// rebuildField rebuilds the documents a field update may change, batch by batch, then ends it.
func (s *PGStore) rebuildField(ctx context.Context, db DB, keyHash []byte, o IndexBuildOptions, batches *int) error {
	s.cats.drop(db)
	for {
		if err := s.serving(); err != nil {
			return err
		}
		if err := step(o, batches); err != nil {
			return err
		}
		cat, err := s.cats.get(ctx, db)
		if err != nil {
			return err
		}
		done := false
		err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
			q := queries.New(tx)
			row, err := q.LockFieldOverride(ctx, queries.LockFieldOverrideParams{ProjectNumber: db.Project, DatabaseID: db.ID, KeyHash: keyHash})
			if errors.Is(err, pgx.ErrNoRows) || err == nil && !row.Building {
				done = true
				return nil
			}
			if err != nil {
				return err
			}
			if cat.prior == nil {
				return errors.New("the catalog has no configuration before the build")
			}
			docs, err := batchDocs(ctx, q, db, row.CollectionGroup, row.BuildCursor, o.Batch)
			if err != nil {
				return err
			}
			if len(docs) == 0 {
				done = true
				return s.fieldBuilt(ctx, q, db, cat, row)
			}
			var dels, ins []entry
			for _, d := range docs {
				p, err := pathOfKey(d.Name)
				if err != nil {
					return err
				}
				fields, _, _, err := decodeDocument(d.Doc)
				if err != nil {
					return err
				}
				now := entriesOf(cat, cat.fields, p, fields, false).set
				before := entriesOf(cat, cat.prior, p, fields, false).set
				for e := range before {
					if _, ok := now[e]; !ok {
						dels = append(dels, e)
					}
				}
				for e := range now {
					if _, ok := before[e]; !ok {
						ins = append(ins, e)
					}
				}
			}
			if err := deleteEntries(ctx, q, db, dels); err != nil {
				return err
			}
			if err := insertEntries(ctx, q, db, ins); err != nil {
				return err
			}
			return q.SetFieldCursor(ctx, queries.SetFieldCursorParams{ProjectNumber: db.Project, DatabaseID: db.ID,
				KeyHash: keyHash, BuildCursor: docs[len(docs)-1].NameKey})
		})
		if err != nil {
			return fmt.Errorf("documents: rebuild field of %d/%s: %w", db.Project, db.ID, err)
		}
		if done {
			s.cats.drop(db)
			return nil
		}
	}
}

// fieldBuilt ends a field update's build: its indexes READY, or the override gone when it
// reverted to its ancestor's, a TTL policy ACTIVE, the next catalog version, the operation done.
func (s *PGStore) fieldBuilt(ctx context.Context, q *queries.Queries, db DB, cat *catalog, row queries.LockFieldOverrideRow) error {
	k := overrideKey{row.CollectionGroup, row.FieldPath}
	u := &fieldUpdate{group: row.CollectionGroup, path: row.FieldPath, keyHash: fieldKeyHash(k.group, k.path), ttlState: deref(row.TtlState)}
	if u.ttlState == "CREATING" {
		u.ttlState = "ACTIVE"
	}
	fields := maps.Clone(cat.fields)
	if row.Reverting {
		delete(fields, k)
	} else {
		ready := make([]index.SingleFieldIndex, len(cat.fields[k]))
		for i, ix := range cat.fields[k] {
			ready[i] = index.SingleFieldIndex{Scope: ix.Scope, Mode: ix.Mode}
		}
		u.config, fields[k] = ready, ready
	}
	if err := u.write(ctx, q, db); err != nil {
		return err
	}
	if err := q.BumpCatalog(ctx, queries.BumpCatalogParams{ProjectNumber: db.Project, DatabaseID: db.ID}); err != nil {
		return err
	}
	if row.OperationID == nil {
		return nil
	}
	op, err := q.LockOperation(ctx, queries.LockOperationParams{ProjectNumber: db.Project, DatabaseID: db.ID, ID: *row.OperationID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	md, err := unmarshalMetadata(op.Metadata)
	if err != nil {
		return err
	}
	if fm, ok := md.(*adminpb.FieldOperationMetadata); ok {
		named := db
		named.ProjectID, _, _ = strings.Cut(strings.TrimPrefix(fm.GetField(), "projects/"), "/")
		return finishOperation(ctx, q, db, *row.OperationID, adminpb.OperationState_SUCCESSFUL, u.response(named, cat, fields))
	}
	return finishOperation(ctx, q, db, *row.OperationID, adminpb.OperationState_SUCCESSFUL, nil)
}
