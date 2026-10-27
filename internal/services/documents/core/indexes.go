package core

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/queries"
	"github.com/arumadev/aruma/internal/services/documents/value"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
)

// Index entries (ADR-024 §1.2, §2.5; ADR-025 §2, §3). Every index a commit writes is a set of
// rows of index_entries, one per (document, value combination), keyed by the index's id and a
// sort key whose bytes order as the index does:
//
//	key        value.KeyVersion
//	           [collection]          COLLECTION scope only: the document's collection
//	           value...              each indexed field's value.Key without its version byte,
//	                                 every byte inverted for a descending field; an
//	                                 array-contains field holds one element
//	           name                  the document's name key without its version byte (as
//	                                 nameKey: per segment, then 0x00), inverted for __name__
//	                                 descending
//	           length                the name's length, 2 bytes big-endian
//	collection the collection's prefix key without its version byte (collectionPrefix), then
//	           0x00: below every key of a deeper collection of the same id
//
// Each part is prefix-free (no encoding of a value or name is a prefix of another, ADR-024
// §1.3), so the keys compare part by part, an inverted part in reverse; the length after the
// name changes no order and lets a scan read the name back without decoding the values. The
// stored key is cut at 2,048 bytes with the whole key's hash, as names are (storedKey).
//
// A composite index's id is its indexes row's. A single-field index has no row: its id is
// SingleFieldIndexID, from its collection group, scope, field and mode, so a commit writes the
// automatic indexes of a field it never saw without touching a cold table, and the planner finds
// an index by its field.

// SingleFieldIndexID returns the id of a single-field index: a negative number, so it never
// meets a composite's, from the SHA-256 of its collection group, field path (canonical, as
// value.FieldPath.String writes it), scope and mode.
func SingleFieldIndexID(group string, scope index.Scope, path string, mode index.FieldMode) int64 {
	h := sha256.New()
	h.Write([]byte(group))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0, byte(scope), byte(mode)})
	sum := h.Sum(nil)
	return -int64(binary.BigEndian.Uint64(sum)>>1) - 1
}

// IndexKeyPrefix returns the start of the keys of an index of scope for the documents of the
// collection id under parent: the collection for COLLECTION scope, nothing but the version byte
// for COLLECTION_GROUP, whose keys span every collection of the group.
func IndexKeyPrefix(scope index.Scope, parent Path, collectionID string) []byte {
	k := []byte{value.KeyVersion}
	if scope == index.ScopeCollection {
		k = appendCollectionPart(k, parent, collectionID)
	}
	return k
}

func appendCollectionPart(dst []byte, parent Path, collectionID string) []byte {
	return append(append(dst, collectionPrefix(parent, collectionID)[1:]...), 0x00)
}

// AppendIndexValue appends a field's part of an index key: v's sort key (with the 1500-byte
// budget) without its version byte, inverted when desc.
func AppendIndexValue(dst []byte, v value.Value, desc bool) []byte {
	start := len(dst)
	dst = value.AppendKey(dst, v)
	dst = append(dst[:start], dst[start+1:]...)
	if desc {
		invert(dst[start:])
	}
	return dst
}

// AppendIndexName appends the document's part of an index key and the length that ends it:
// a scan bound may stop before the length, which every key of the document continues with.
func AppendIndexName(dst []byte, p Path, desc bool) []byte {
	start := len(dst)
	dst = append(dst, nameKey(p)[1:]...)
	n := len(dst) - start
	if desc {
		invert(dst[start:])
	}
	return binary.BigEndian.AppendUint16(dst, uint16(n))
}

// IndexPrefixEnd returns the exclusive end of the keys that start with prefix, a key built to a
// part's end with the functions above: no part starts with 0xFF.
func IndexPrefixEnd(prefix []byte) []byte { return append(bytes.Clone(prefix), 0xFF) }

func invert(b []byte) {
	for i := range b {
		b[i] = ^b[i]
	}
}

// pathOfEntry returns the document of a whole index key.
func pathOfEntry(k []byte) (Path, error) {
	if len(k) < 4 {
		return nil, errBadKey
	}
	n := int(binary.BigEndian.Uint16(k[len(k)-2:]))
	if n == 0 || n > len(k)-3 {
		return nil, errBadKey
	}
	name := append([]byte{value.KeyVersion}, k[len(k)-2-n:len(k)-2]...)
	if name[1] >= 0x80 { // an inverted segment mark: 0xFE or 0xFD
		invert(name[1:])
	}
	return pathOfKey(name)
}

// composite is a composite index of the catalog.
type composite struct {
	id     int64
	group  string
	scope  index.Scope
	fields []compositeField // the last is __name__
	ready  bool
	// vector: a field has a vector_config. Such an index is stored and listed, never written
	// nor served (ADR-025 §3).
	vector bool
	spec   *adminpb.Index
	// createTime and readyTime are the row's; operation is the CreateIndex operation's id.
	createTime time.Time
	readyTime  time.Time
	operation  string
}

type compositeField struct {
	path  value.FieldPath
	canon string
	mode  index.FieldMode
}

// catalog is one database's index configuration at one catalog_version: what a commit writes
// and what the planner may use.
type catalog struct {
	version int64
	// found is false for a database without a sequencer row.
	found      bool
	composites []*composite // CREATING and READY, by id
	// fields is the field configuration commits write. prior, set while a field configuration
	// is being built, is the configuration before that change: its entries may still be stored,
	// so a commit deletes them too (indexbuild.go).
	fields fieldConfigs
	prior  fieldConfigs
	// overrides is the stored rows, for the Admin API.
	overrides []overrideRow
	planner   *index.Catalog
	// ttl is the TTL policies by collection group, CREATING and ACTIVE (ttl.go).
	ttl map[string][]ttlPolicy
}

// overrideRow is a field_overrides row as the catalog read it.
type overrideRow struct {
	group, path string
	config      *adminpb.Field_IndexConfig // nil: inherits
	ttlState    string
	building    bool
	reverting   bool
	updateTime  time.Time
	etag        string
	operation   string
}

// byGroup returns the composites a document of collection id group belongs to.
func (c *catalog) byGroup(group string) []*composite {
	var out []*composite
	for _, ix := range c.composites {
		if ix.group == group && !ix.vector {
			out = append(out, ix)
		}
	}
	return out
}

// catalogs caches each database's catalog. Only this process changes a catalog (it holds the
// single-instance lock, ADR-024 §7), and drops the entry when it does. A commit checks the
// version it computed with at its stamp, so a stale entry costs a retry, never a wrong entry.
type catalogs struct {
	pool *pgxpool.Pool
	mu   sync.Mutex
	m    map[dbKey]*catalog
	gen  map[dbKey]uint64
}

func newCatalogs(pool *pgxpool.Pool) *catalogs {
	return &catalogs{pool: pool, m: map[dbKey]*catalog{}, gen: map[dbKey]uint64{}}
}

// get returns db's catalog, loading it when it is not cached.
func (c *catalogs) get(ctx context.Context, db DB) (*catalog, error) {
	k := dbKey{db.Project, db.ID}
	c.mu.Lock()
	cat, gen := c.m[k], c.gen[k]
	c.mu.Unlock()
	if cat != nil {
		return cat, nil
	}
	cat, err := loadCatalog(ctx, c.pool, db)
	if err != nil || !cat.found {
		return cat, err
	}
	c.mu.Lock()
	// A load that began before a change must not cache what it read.
	if c.gen[k] == gen {
		c.m[k] = cat
	}
	c.mu.Unlock()
	return cat, nil
}

// drop forgets db's catalog: it changed, or a commit found it stale.
func (c *catalogs) drop(db DB) {
	k := dbKey{db.Project, db.ID}
	c.mu.Lock()
	delete(c.m, k)
	c.gen[k]++
	c.mu.Unlock()
}

// loadCatalog reads db's catalog version, composite indexes and field overrides in one snapshot.
func loadCatalog(ctx context.Context, pool *pgxpool.Pool, db DB) (*catalog, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	return readCatalog(ctx, queries.New(tx), db)
}

// readCatalog reads db's catalog with q, in q's snapshot.
func readCatalog(ctx context.Context, q *queries.Queries, db DB) (*catalog, error) {
	head, err := q.ReadCatalog(ctx, queries.ReadCatalogParams{ProjectNumber: db.Project, DatabaseID: db.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return &catalog{}, nil
	}
	if err != nil {
		return nil, err
	}
	ixs, err := q.CatalogIndexes(ctx, queries.CatalogIndexesParams{ProjectNumber: db.Project, DatabaseID: db.ID})
	if err != nil {
		return nil, err
	}
	ovs, err := q.CatalogOverrides(ctx, queries.CatalogOverridesParams{ProjectNumber: db.Project, DatabaseID: db.ID})
	if err != nil {
		return nil, err
	}
	cat := &catalog{version: head.CatalogVersion, found: true, fields: fieldConfigs{}}
	for _, r := range ixs {
		ix, err := decodeComposite(r.ID, r.CollectionGroup, r.QueryScope, r.Spec, r.State)
		if err != nil {
			return nil, fmt.Errorf("documents: index %d of %d/%s: %w", r.ID, db.Project, db.ID, err)
		}
		ix.createTime, ix.operation = r.CreateTime, deref(r.OperationID)
		if r.ReadyTime != nil {
			ix.readyTime = *r.ReadyTime
		}
		cat.composites = append(cat.composites, ix)
	}
	var building *queries.CatalogOverridesRow
	for _, r := range ovs {
		row := overrideRow{group: r.CollectionGroup, path: r.FieldPath, ttlState: deref(r.TtlState), building: r.Building,
			reverting: r.Reverting, updateTime: r.UpdateTime, etag: r.Etag, operation: deref(r.OperationID)}
		if r.IndexConfig != nil {
			if row.config, err = decodeIndexConfig(r.IndexConfig); err != nil {
				return nil, fmt.Errorf("documents: field override %s/%s: %w", r.CollectionGroup, r.FieldPath, err)
			}
			if cat.fields[overrideKey{r.CollectionGroup, r.FieldPath}], err = singleFieldIndexes(row.config); err != nil {
				return nil, err
			}
		}
		if r.Building {
			building = &r
		}
		if row.ttlState != "" {
			if err := cat.addTTL(row); err != nil {
				return nil, fmt.Errorf("documents: TTL policy %s/%s: %w", r.CollectionGroup, r.FieldPath, err)
			}
		}
		cat.overrides = append(cat.overrides, row)
	}
	if building != nil {
		// The configuration before the change being built: the building row's prior one.
		k := overrideKey{building.CollectionGroup, building.FieldPath}
		cat.prior = maps.Clone(cat.fields)
		delete(cat.prior, k)
		if building.PriorIndexConfig != nil {
			prior, err := decodeIndexConfig(building.PriorIndexConfig)
			if err != nil {
				return nil, err
			}
			if cat.prior[k], err = singleFieldIndexes(prior); err != nil {
				return nil, err
			}
		}
	}
	if cat.planner, err = plannerCatalog(cat); err != nil {
		return nil, err
	}
	return cat, nil
}

// plannerCatalog is what package index decides queries with: the composites but the vector
// ones, CREATING ones marked so (R21), and the field overrides with their indexes' states.
func plannerCatalog(cat *catalog) (*index.Catalog, error) {
	var ixs []index.Index
	for _, c := range cat.composites {
		if c.vector {
			continue
		}
		ix := index.Index{CollectionGroup: c.group, Scope: c.scope, State: index.StateCreating}
		if c.ready {
			ix.State = index.StateReady
		}
		for _, f := range c.fields {
			ix.Fields = append(ix.Fields, index.IndexField{Path: f.canon, Mode: f.mode})
		}
		ixs = append(ixs, ix)
	}
	var ovs []index.FieldOverride
	for k, v := range cat.fields {
		ovs = append(ovs, index.FieldOverride{CollectionGroup: k.group, Path: k.path, Indexes: v})
	}
	slices.SortFunc(ovs, func(a, b index.FieldOverride) int {
		return cmp.Or(strings.Compare(a.CollectionGroup, b.CollectionGroup), strings.Compare(a.Path, b.Path))
	})
	return index.NewCatalog(ixs, ovs)
}

// decodeComposite reads an indexes row's spec.
func decodeComposite(id int64, group string, scope int16, spec []byte, state string) (*composite, error) {
	pb := &adminpb.Index{}
	if err := proto.Unmarshal(spec, pb); err != nil {
		return nil, err
	}
	ix := &composite{id: id, group: group, scope: index.Scope(scope), spec: pb, ready: state == "READY"}
	for _, f := range pb.GetFields() {
		path, err := value.ParseFieldPath(f.GetFieldPath())
		if err != nil {
			return nil, err
		}
		cf := compositeField{path: path, canon: path.String()}
		switch {
		case f.GetVectorConfig() != nil:
			ix.vector = true
			continue
		case f.GetArrayConfig() == adminpb.Index_IndexField_CONTAINS:
			cf.mode = index.Contains
		case f.GetOrder() == adminpb.Index_IndexField_DESCENDING:
			cf.mode = index.Descending
		case f.GetOrder() == adminpb.Index_IndexField_ASCENDING:
			cf.mode = index.Ascending
		default:
			return nil, fmt.Errorf("field %s has no mode", f.GetFieldPath())
		}
		ix.fields = append(ix.fields, cf)
	}
	return ix, nil
}

func decodeIndexConfig(b []byte) (*adminpb.Field_IndexConfig, error) {
	c := &adminpb.Field_IndexConfig{}
	return c, proto.Unmarshal(b, c)
}

// singleFieldIndexes returns a stored IndexConfig's indexes.
func singleFieldIndexes(c *adminpb.Field_IndexConfig) ([]index.SingleFieldIndex, error) {
	out := []index.SingleFieldIndex{}
	for _, ix := range c.GetIndexes() {
		sf, err := singleFieldOf(ix)
		if err != nil {
			return nil, err
		}
		if ix.GetState() == adminpb.Index_CREATING {
			sf.State = index.StateCreating
		}
		out = append(out, sf)
	}
	return out, nil
}

// singleFieldOf returns the scope and mode of an override's index.
func singleFieldOf(ix *adminpb.Index) (index.SingleFieldIndex, error) {
	sf := index.SingleFieldIndex{Scope: index.Scope(ix.GetQueryScope())}
	if len(ix.GetFields()) != 1 {
		return sf, errors.New("a single-field index needs exactly one field")
	}
	switch f := ix.GetFields()[0]; {
	case f.GetArrayConfig() == adminpb.Index_IndexField_CONTAINS:
		sf.Mode = index.Contains
	case f.GetOrder() == adminpb.Index_IndexField_ASCENDING:
		sf.Mode = index.Ascending
	case f.GetOrder() == adminpb.Index_IndexField_DESCENDING:
		sf.Mode = index.Descending
	default:
		return sf, errors.New("a single-field index needs an order or array_config CONTAINS")
	}
	if sf.Scope != index.ScopeCollection && sf.Scope != index.ScopeCollectionGroup {
		return sf, errors.New("a single-field index needs the scope COLLECTION or COLLECTION_GROUP")
	}
	return sf, nil
}

// catalog returns db's catalog, creating '(default)' when it does not exist.
func (s *PGStore) catalog(ctx context.Context, db DB) (*catalog, error) {
	cat, err := s.cats.get(ctx, db)
	if err != nil || cat.found {
		return cat, err
	}
	if err := s.missing(ctx, db); err != nil {
		return nil, err
	}
	if cat, err = s.cats.get(ctx, db); err == nil && !cat.found {
		return nil, &DatabaseNotFoundError{ProjectID: db.ProjectID, DatabaseID: db.ID}
	}
	return cat, err
}

// IndexCatalog returns the index configuration the planner decides db's queries with
// (index.Check): its composite indexes, CREATING ones marked so, never DELETING ones, and its
// field overrides with their indexes' states. It is cached and changes with every index or
// override change; a nil error always comes with a catalog.
func (s *PGStore) IndexCatalog(ctx context.Context, db DB) (*index.Catalog, error) {
	if err := s.serving(); err != nil {
		return nil, err
	}
	cat, err := s.catalog(ctx, db)
	if err != nil {
		return nil, err
	}
	return cat.planner, nil
}

// CompositeIndexID returns the id of db's READY composite index that is ix once normalized
// (index.Index.Normalize), for ScanIndex; ok is false when there is none.
func (s *PGStore) CompositeIndexID(ctx context.Context, db DB, ix index.Index) (id int64, ok bool, err error) {
	cat, err := s.catalog(ctx, db)
	if err != nil {
		return 0, false, err
	}
	ix = ix.Normalize()
	for _, c := range cat.composites {
		if !c.ready || c.vector || c.group != ix.CollectionGroup || c.scope != ix.Scope || len(c.fields) != len(ix.Fields) {
			continue
		}
		same := true
		for i, f := range c.fields {
			if f.canon != ix.Fields[i].Path || f.mode != ix.Fields[i].Mode {
				same = false
				break
			}
		}
		if same {
			return c.id, true, nil
		}
	}
	return 0, false, nil
}

// IndexRange is a scan of one index (ScanIndex): the entries whose whole key k has
// Lo <= k < Hi, in key order. Lo nil starts at the first entry, Hi nil ends after the last;
// both are built with IndexKeyPrefix, AppendIndexValue, AppendIndexName and IndexPrefixEnd.
// Limit, when positive, is the most entries returned.
type IndexRange struct {
	Index  int64
	Lo, Hi []byte
	Limit  int
}

// IndexHit is one entry a scan returned: its whole key and its document.
type IndexHit struct {
	Key  []byte
	Path Path
}

// scanPage is the most stored entries one scan statement reads.
const scanPage = 1000

// errIndexNotReady is a scan of a composite index that is not READY: the planner must never
// serve one (ADR-024 §2.5).
var errIndexNotReady = errors.New("documents: scan of an index that is not READY")

// ScanIndex returns the entries of an index in r, ordered by whole key, at the latest commit,
// and that commit's commit_time. A composite index must be READY; a single-field index's
// readiness is the catalog's (index.Check). Keys that share their first 2,048 bytes are stored in
// hash order: each such run is read whole and ordered by the whole key before the limit applies
// (ADR-024 §1.4). A read at a read_time is not served from index entries, which hold the latest
// state only: such queries take the reference path. The planner reads through BeginIndexRead,
// which holds its scans and documents to one snapshot.
func (s *PGStore) ScanIndex(ctx context.Context, db DB, r IndexRange) ([]IndexHit, time.Time, error) {
	rd, err := s.BeginIndexRead(ctx, db)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rd.Close(ctx)
	hits, _, err := rd.Scan(ctx, r)
	if err != nil {
		return nil, time.Time{}, err
	}
	return hits, rd.ReadTime(), nil
}

// scanEntries reads the entries of r with q, in q's snapshot.
func scanEntries(ctx context.Context, q *queries.Queries, db DB, r IndexRange) ([]IndexHit, int, error) {
	lo, hi := []byte{}, []byte{value.KeyVersion + 1} // every key starts with the version byte
	if r.Lo != nil {
		lo = lowerBound(r.Lo)
	}
	if r.Hi != nil {
		hi = upperBound(r.Hi)
	}
	in := func(k []byte) bool {
		return (r.Lo == nil || bytes.Compare(k, r.Lo) >= 0) && (r.Hi == nil || bytes.Compare(k, r.Hi) < 0)
	}
	var hits []IndexHit
	read := 0
	for r.Limit <= 0 || len(hits) < r.Limit {
		// A page reads no more than the limit still wants, so a scan under a limit reads about
		// as many entries as it returns.
		n := scanPage
		if r.Limit > 0 {
			n = min(n, r.Limit-len(hits))
		}
		rows, err := q.ScanIndexEntries(ctx, queries.ScanIndexEntriesParams{ProjectNumber: db.Project, DatabaseID: db.ID,
			IndexID: r.Index, Lo: lo, Hi: hi, RowLimit: int32(n)})
		if err != nil {
			return nil, 0, err
		}
		if len(rows) == 0 {
			break
		}
		full := len(rows) == n
		last := rows[len(rows)-1].Key
		if full && len(last) > cutLen {
			// The page ends inside a run of keys cut alike: read the rest of the run.
			_, runHi := chainBounds(last)
			more, err := q.ScanIndexEntries(ctx, queries.ScanIndexEntriesParams{ProjectNumber: db.Project, DatabaseID: db.ID,
				IndexID: r.Index, Lo: append(bytes.Clone(last), 0x00), Hi: append(runHi, 0x00), RowLimit: 1 << 30})
			if err != nil {
				return nil, 0, err
			}
			rows = append(rows, more...)
			last = rows[len(rows)-1].Key
		}
		read += len(rows)
		page := make([]IndexHit, 0, len(rows))
		for _, row := range rows {
			k := row.Key
			if row.FullKey != nil {
				k = row.FullKey
			}
			if !in(k) {
				continue
			}
			p, err := pathOfEntry(k)
			if err != nil {
				return nil, 0, err
			}
			page = append(page, IndexHit{Key: k, Path: p})
		}
		slices.SortFunc(page, func(a, b IndexHit) int { return bytes.Compare(a.Key, b.Key) })
		hits = append(hits, page...)
		if !full {
			break
		}
		lo = append(bytes.Clone(last), 0x00)
	}
	if r.Limit > 0 && len(hits) > r.Limit {
		hits = hits[:r.Limit]
	}
	return hits, read, nil
}
