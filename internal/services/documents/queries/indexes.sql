-- The index catalogue, its builds and the Admin API's operations (ADR-024 §2.5; ADR-025 §2 to
-- §4.5; core/indexes.go, core/indexbuild.go, core/indexadmin.go). The commit's own index
-- statements are in commit.sql.

-- name: CatalogIndexes :many
-- A database's composite indexes that commits write or the planner may use: every one but those
-- being deleted. Read with CatalogOverrides and ReadCatalog in one snapshot.
SELECT id, collection_group, query_scope, spec, state, create_time, ready_time, operation_id
FROM documents.indexes
WHERE project_number = @project_number AND database_id = @database_id AND kind = 'composite'
    AND state <> 'DELETING'
ORDER BY id;

-- name: CatalogOverrides :many
SELECT collection_group, field_path, index_config, ttl_state, building, prior_index_config, reverting,
    update_time, etag, operation_id
FROM documents.field_overrides
WHERE project_number = @project_number AND database_id = @database_id
ORDER BY collection_group, field_path;

-- name: BumpCatalog :exec
-- A change to the set of indexes commits write: it takes the sequencer row, so it is ordered
-- with every stamp, and a commit that computed its entries before it fails its stamp.
UPDATE documents.sequencer SET catalog_version = catalog_version + 1
WHERE project_number = @project_number AND database_id = @database_id;

-- name: CountComposites :one
SELECT count(*) FROM documents.indexes
WHERE project_number = @project_number AND database_id = @database_id AND kind = 'composite'
    AND state <> 'DELETING';

-- name: InsertComposite :many
-- A new composite index; no row when one of the same collection group, scope and spec exists.
INSERT INTO documents.indexes (project_number, database_id, kind, collection_group, query_scope, spec,
    spec_hash, state, create_time, ready_time, operation_id)
SELECT @project_number, @database_id, 'composite', @collection_group, @query_scope, @spec, @spec_hash,
    @state, t.now, CASE WHEN @state::text = 'READY' THEN t.now END, sqlc.narg(operation_id)
FROM (SELECT clock_timestamp() AS now) t
ON CONFLICT DO NOTHING
RETURNING id, create_time;

-- name: CompositeBySpec :one
SELECT id FROM documents.indexes
WHERE project_number = @project_number AND database_id = @database_id AND collection_group = @collection_group
    AND query_scope = @query_scope AND spec_hash = @spec_hash;

-- name: GetComposite :one
SELECT id, collection_group, query_scope, spec, state, create_time, ready_time, operation_id
FROM documents.indexes
WHERE project_number = @project_number AND database_id = @database_id AND id = @id AND kind = 'composite'
    AND state <> 'DELETING';

-- name: MarkCompositeDeleting :one
-- Takes an index out of every commit and of the planner; its entries are purged afterwards. Its
-- spec_hash moves aside, so the same index may be created again before the purge ends.
UPDATE documents.indexes SET state = 'DELETING', build_cursor = NULL,
    spec_hash = sha256(spec_hash || int8send(id))
WHERE project_number = @project_number AND database_id = @database_id AND id = @id
    AND collection_group = @collection_group AND kind = 'composite' AND state <> 'DELETING'
RETURNING operation_id;

-- name: PendingIndexBuilds :many
-- scope: global — the build job visits every database's composite indexes being built or deleted.
SELECT project_number, database_id, id, state FROM documents.indexes
WHERE kind = 'composite' AND state IN ('CREATING', 'DELETING')
ORDER BY project_number, database_id, id;

-- name: LockCompositeBuild :one
-- An index as a build batch starts, locked, so that DeleteIndex waits for the batch to end.
SELECT state, collection_group, query_scope, spec, build_cursor, operation_id
FROM documents.indexes
WHERE project_number = @project_number AND database_id = @database_id AND id = @id
FOR UPDATE;

-- name: BackfillGroup :many
-- A batch of a collection group's documents after a stored name key, locked FOR SHARE: a commit
-- of one of them, which takes it FOR UPDATE, comes wholly before or after the batch (ADR-024
-- §2.5). A document deleted meanwhile is skipped, so only an empty batch ends a build.
SELECT name_key, name, doc FROM documents.documents
WHERE project_number = @project_number AND database_id = @database_id AND cg_hash = @cg_hash
    AND collection_id = @collection_id AND name_key > @after
ORDER BY cg_hash, name_key
LIMIT @batch
FOR SHARE;

-- name: BackfillAll :many
-- As BackfillGroup, over every document of the database: a change of the database default.
SELECT name_key, name, doc FROM documents.documents
WHERE project_number = @project_number AND database_id = @database_id AND name_key > @after
ORDER BY name_key
LIMIT @batch
FOR SHARE;

-- name: SetCompositeCursor :exec
UPDATE documents.indexes SET build_cursor = @build_cursor
WHERE project_number = @project_number AND database_id = @database_id AND id = @id;

-- name: MarkCompositeReady :one
UPDATE documents.indexes SET state = 'READY', ready_time = clock_timestamp(), build_cursor = NULL
WHERE project_number = @project_number AND database_id = @database_id AND id = @id AND state = 'CREATING'
RETURNING ready_time;

-- name: PurgeCompositeEntries :execrows
-- Up to @batch entries of an index being deleted.
DELETE FROM documents.index_entries t USING (
    SELECT x.key FROM documents.index_entries x
    WHERE x.project_number = @project_number AND x.database_id = @database_id AND x.index_id = @index_id
    LIMIT @batch
) d
WHERE t.project_number = @project_number AND t.database_id = @database_id AND t.index_id = @index_id
    AND t.key = d.key;

-- name: DeleteComposite :exec
-- The row of an index whose entries are all purged.
DELETE FROM documents.indexes
WHERE project_number = @project_number AND database_id = @database_id AND id = @id AND state = 'DELETING';

-- name: LockFieldOverride :one
SELECT collection_group, field_path, index_config, ttl_state, building, prior_index_config, reverting,
    build_cursor, update_time, etag, operation_id
FROM documents.field_overrides
WHERE project_number = @project_number AND database_id = @database_id AND key_hash = @key_hash
FOR UPDATE;

-- name: UpsertFieldOverride :exec
INSERT INTO documents.field_overrides (project_number, database_id, key_hash, collection_group, field_path,
    index_config, ttl_state, update_time, etag, building, prior_index_config, reverting, build_cursor, operation_id)
VALUES (@project_number, @database_id, @key_hash, @collection_group, @field_path, @index_config, sqlc.narg(ttl_state),
    clock_timestamp(), @etag, @building, @prior_index_config, @reverting, @build_cursor, sqlc.narg(operation_id))
ON CONFLICT (project_number, database_id, key_hash) DO UPDATE SET
    index_config = excluded.index_config, ttl_state = excluded.ttl_state, update_time = excluded.update_time,
    etag = excluded.etag, building = excluded.building, prior_index_config = excluded.prior_index_config,
    reverting = excluded.reverting, build_cursor = excluded.build_cursor, operation_id = excluded.operation_id;

-- name: SetFieldCursor :exec
UPDATE documents.field_overrides SET build_cursor = @build_cursor
WHERE project_number = @project_number AND database_id = @database_id AND key_hash = @key_hash;

-- name: DeleteFieldOverride :exec
DELETE FROM documents.field_overrides
WHERE project_number = @project_number AND database_id = @database_id AND key_hash = @key_hash;

-- name: CountFieldOverrides :one
-- The overrides that count against documents.limits.field_overrides: every explicit one but the
-- database default.
SELECT count(*) FROM documents.field_overrides
WHERE project_number = @project_number AND database_id = @database_id
    AND collection_group <> '__default__' AND (index_config IS NOT NULL OR ttl_state IS NOT NULL);

-- name: PendingFieldBuilds :many
-- scope: global — the build job visits every database's field configuration being built, then
-- the oldest queued field update of each database with none.
SELECT f.project_number, f.database_id, f.key_hash, ''::text AS operation_id FROM documents.field_overrides f
WHERE f.building
UNION ALL
SELECT q.project_number, q.database_id, ''::bytea, q.id FROM (
    SELECT DISTINCT ON (o.project_number, o.database_id) o.project_number, o.database_id, o.id
    FROM documents.operations o
    WHERE o.request IS NOT NULL
    ORDER BY o.project_number, o.database_id, o.create_time, o.id
) q
WHERE NOT EXISTS (SELECT 1 FROM documents.field_overrides b
    WHERE b.project_number = q.project_number AND b.database_id = q.database_id AND b.building)
ORDER BY 1, 2;

-- name: InsertOperation :exec
INSERT INTO documents.operations (project_number, database_id, id, kind, metadata, done, result, create_time, request)
VALUES (@project_number, @database_id, @id, @kind, @metadata, @done, @result, clock_timestamp(), @request);

-- name: LockOperation :one
SELECT kind, metadata, done, result, create_time, request FROM documents.operations
WHERE project_number = @project_number AND database_id = @database_id AND id = @id
FOR UPDATE;

-- name: UpdateOperation :exec
UPDATE documents.operations SET metadata = @metadata, done = @done, result = @result, request = @request
WHERE project_number = @project_number AND database_id = @database_id AND id = @id;

-- name: GetOperation :one
SELECT kind, metadata, done, result, create_time FROM documents.operations
WHERE project_number = @project_number AND database_id = @database_id AND id = @id;

-- name: ListOperations :many
SELECT id, kind, metadata, done, result, create_time FROM documents.operations
WHERE project_number = @project_number AND database_id = @database_id
ORDER BY create_time, id;

-- name: PruneOperations :execrows
-- Deletes up to @batch of a database's operations done more than @retention_secs ago.
DELETE FROM documents.operations o USING (
    SELECT x.id FROM documents.operations x
    WHERE x.project_number = @project_number AND x.database_id = @database_id AND x.done
        AND x.create_time < now() - make_interval(secs => @retention_secs::float8)
    LIMIT @batch
) d
WHERE o.project_number = @project_number AND o.database_id = @database_id AND o.id = d.id;

-- name: ScanIndexEntries :many
-- An index's entries with a stored key in [@lo, @hi), in key order: the planner's scan.
SELECT key, full_key FROM documents.index_entries
WHERE project_number = @project_number AND database_id = @database_id AND index_id = @index_id
    AND key >= @lo AND key < @hi
ORDER BY key
LIMIT @row_limit;

-- name: AllIndexEntries :many
-- Every entry of a database, for the consistency check (CheckIndexEntries), in a snapshot.
SELECT index_id, key, full_key FROM documents.index_entries
WHERE project_number = @project_number AND database_id = @database_id
ORDER BY index_id, key;

-- name: AllDocuments :many
SELECT name, doc FROM documents.documents
WHERE project_number = @project_number AND database_id = @database_id
ORDER BY name_key;
