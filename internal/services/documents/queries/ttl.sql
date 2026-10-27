-- The TTL sweeper (core/ttl.go; ADR-028 §2): it finds the documents due by documents_expire and
-- deletes each through the commit path.

-- name: TTLDatabases :many
-- scope: global — the sweeper visits every database with an ACTIVE TTL policy.
SELECT DISTINCT project_number, database_id FROM documents.field_overrides
WHERE ttl_state = 'ACTIVE'
ORDER BY project_number, database_id;

-- name: DueDocuments :many
-- A batch of a database's documents whose expire_time is at or before @now, after the
-- (expire_time, name_key) the last batch ended at, by documents_expire.
SELECT name_key, name, doc, update_time, expire_time::timestamptz AS expire_time FROM documents.documents
WHERE project_number = @project_number AND database_id = @database_id
    AND expire_time IS NOT NULL AND expire_time <= @now::timestamptz
    AND (expire_time, name_key) > (@after_expire::timestamptz, @after_key::bytea)
ORDER BY expire_time, name_key
LIMIT @batch;
