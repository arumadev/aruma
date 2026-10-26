-- Retention of versions and the changelog (ADR-024 §8), and the purge of a deleted project's
-- rows (ADR-024 §1.1). Every delete takes at most @batch rows, so that no statement holds many
-- row locks or runs long; the caller repeats it.

-- name: ListAllDatabases :many
-- scope: global — retention visits every database of every project.
SELECT project_number, database_id FROM documents.databases ORDER BY project_number, database_id;

-- name: PruneVersions :execrows
-- Deletes up to @batch versions of a database that ended before its read_time window, less a
-- minute's margin for a read whose window was checked just before.
DELETE FROM documents.versions v USING (
    SELECT x.project_number, x.database_id, x.name_key, x.end_seq
    FROM documents.versions x
    JOIN documents.databases b ON b.project_number = x.project_number AND b.database_id = x.database_id
    WHERE x.project_number = @project_number AND x.database_id = @database_id
      AND x.end_time < now() - b.version_retention - interval '1 minute'
    LIMIT @batch
) d
WHERE v.project_number = d.project_number AND v.database_id = d.database_id
  AND v.name_key = d.name_key AND v.end_seq = d.end_seq;

-- name: PruneChangelog :execrows
-- Deletes up to @batch of a database's changelog rows committed more than @retention_secs ago.
-- Commit time rises with seq, so they are a prefix of the seq order, which the primary key
-- reads in order: the oldest @batch rows are taken, and those old enough deleted.
DELETE FROM documents.changelog c USING (
    SELECT x.project_number, x.database_id, x.seq, x.name_key FROM documents.changelog x
    WHERE x.project_number = @project_number AND x.database_id = @database_id
    ORDER BY x.seq, x.name_key
    LIMIT @batch
) d
WHERE c.project_number = d.project_number AND c.database_id = d.database_id
  AND c.seq = d.seq AND c.name_key = d.name_key
  AND c.commit_time < now() - make_interval(secs => @retention_secs::float8);

-- name: PurgeDocuments :execrows
DELETE FROM documents.documents t USING (
    SELECT x.project_number, x.database_id, x.name_key FROM documents.documents x
    WHERE x.project_number = @project_number LIMIT @batch
) d
WHERE t.project_number = d.project_number AND t.database_id = d.database_id AND t.name_key = d.name_key;

-- name: PurgeVersions :execrows
DELETE FROM documents.versions t USING (
    SELECT x.project_number, x.database_id, x.name_key, x.end_seq FROM documents.versions x
    WHERE x.project_number = @project_number LIMIT @batch
) d
WHERE t.project_number = d.project_number AND t.database_id = d.database_id
  AND t.name_key = d.name_key AND t.end_seq = d.end_seq;

-- name: PurgeChangelog :execrows
DELETE FROM documents.changelog t USING (
    SELECT x.project_number, x.database_id, x.seq, x.name_key FROM documents.changelog x
    WHERE x.project_number = @project_number LIMIT @batch
) d
WHERE t.project_number = d.project_number AND t.database_id = d.database_id
  AND t.seq = d.seq AND t.name_key = d.name_key;

-- name: PurgeIndexEntries :execrows
DELETE FROM documents.index_entries t USING (
    SELECT x.project_number, x.database_id, x.index_id, x.key FROM documents.index_entries x
    WHERE x.project_number = @project_number LIMIT @batch
) d
WHERE t.project_number = d.project_number AND t.database_id = d.database_id
  AND t.index_id = d.index_id AND t.key = d.key;

-- name: PurgeUsage :exec
-- A row a day per database: few, so one statement.
DELETE FROM documents.usage WHERE project_number = @project_number;

-- name: PurgeDatabases :exec
-- Last: its cascade removes the sequencer, indexes, field_overrides and operations rows.
DELETE FROM documents.databases WHERE project_number = @project_number;
