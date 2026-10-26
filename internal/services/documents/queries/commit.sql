-- The commit path (ADR-024 §2.1). core queues these in pgx batches rather than calling the
-- generated methods (core/statements.go): the first batch opens the transaction, sets its
-- timeouts, locks the written documents and reads the catalog; the second writes the index
-- entries, stamps and COMMITs in one round trip, so that only the stamp statement and COMMIT run
-- while the sequencer row is held.

-- name: ReadCatalog :one
-- The set of indexes this commit writes, as of now, and the last commit_time: run after
-- LockDocuments, its snapshot holds every commit that wrote the locked documents. No lock: the
-- stamp checks the catalog again.
SELECT catalog_version, last_ts FROM documents.sequencer
WHERE project_number = @project_number AND database_id = @database_id;

-- name: LockDocuments :many
-- The written documents as they are, locked in name-key order so that two commits never wait
-- on each other in Postgres. A document that does not exist returns no row.
SELECT name_key, update_seq, doc, create_time, update_time
FROM documents.documents
WHERE project_number = @project_number AND database_id = @database_id AND name_key = ANY(@name_keys::bytea[])
ORDER BY name_key
FOR UPDATE;

-- name: DeleteIndexEntries :exec
-- An index entry the documents' images before the commit had and their images after do not.
DELETE FROM documents.index_entries e
USING generate_subscripts(@keys::bytea[], 1) AS x(i)
WHERE e.project_number = @project_number AND e.database_id = @database_id
    AND e.index_id = (@index_ids::bigint[])[x.i] AND e.key = (@keys::bytea[])[x.i];

-- name: InsertIndexEntries :exec
INSERT INTO documents.index_entries (project_number, database_id, index_id, key, full_key)
SELECT @project_number, @database_id, (@index_ids::bigint[])[x.i], (@keys::bytea[])[x.i], (@full_keys::bytea[])[x.i]
FROM generate_subscripts(@keys::bytea[], 1) AS x(i)
ON CONFLICT DO NOTHING;

-- name: StampCommit :many
-- The one statement under the sequencer row: the stamp, the documents, the images they replace
-- and the changelog. clock_timestamp(), never now(): now() is the transaction's start, before
-- the locks were waited for. Every write names the update_seq the commit read (inserts expect no
-- row), and conflict_unless fails the statement with 40001 unless each found it (an empty array
-- arrives as NULL, hence the coalesce). No row returned means the catalog changed since
-- ReadCatalog: core rolls back and computes the commit again.
WITH s AS (
    UPDATE documents.sequencer q SET seq = q.seq + 1,
        last_ts = greatest(clock_timestamp(), q.last_ts + interval '1 microsecond')
    WHERE q.project_number = @project_number AND q.database_id = @database_id
        AND q.catalog_version = @catalog_version
    RETURNING q.seq, q.last_ts
), v AS (
    INSERT INTO documents.versions (project_number, database_id, name_key, end_seq, name, parent_hash,
        cg_hash, parent, collection_id, begin_time, end_time, create_time, doc)
    SELECT d.project_number, d.database_id, d.name_key, s.seq, d.name, d.parent_hash,
        d.cg_hash, d.parent, d.collection_id, d.update_time, s.last_ts, d.create_time, d.doc
    FROM s, documents.documents d
    WHERE d.project_number = @project_number AND d.database_id = @database_id
        AND d.name_key = ANY(@replaced_keys::bytea[])
    RETURNING 1
), u AS (
    UPDATE documents.documents d SET doc = (@update_docs::bytea[])[w.i], size = (@update_sizes::integer[])[w.i],
        update_time = s.last_ts, update_seq = s.seq
    FROM s, generate_subscripts(@update_keys::bytea[], 1) AS w(i)
    WHERE d.project_number = @project_number AND d.database_id = @database_id
        AND d.name_key = (@update_keys::bytea[])[w.i] AND d.update_seq = (@update_seqs::bigint[])[w.i]
    RETURNING 1
), i AS (
    INSERT INTO documents.documents (project_number, database_id, name_key, name, parent_hash, cg_hash,
        parent, collection_id, doc, create_time, update_time, update_seq, size)
    SELECT @project_number, @database_id, (@insert_keys::bytea[])[w.i], (@insert_names::bytea[])[w.i],
        (@insert_parent_hashes::bytea[])[w.i], (@insert_cg_hashes::bytea[])[w.i], (@insert_parents::bytea[])[w.i],
        (@insert_collection_ids::text[])[w.i], (@insert_docs::bytea[])[w.i], s.last_ts, s.last_ts, s.seq,
        (@insert_sizes::integer[])[w.i]
    FROM s, generate_subscripts(@insert_keys::bytea[], 1) AS w(i)
    ON CONFLICT DO NOTHING
    RETURNING 1
), x AS (
    DELETE FROM documents.documents d
    USING s, generate_subscripts(@delete_keys::bytea[], 1) AS w(i)
    WHERE d.project_number = @project_number AND d.database_id = @database_id
        AND d.name_key = (@delete_keys::bytea[])[w.i] AND d.update_seq = (@delete_seqs::bigint[])[w.i]
    RETURNING 1
), c AS (
    INSERT INTO documents.changelog (project_number, database_id, seq, name_key, name, commit_time,
        before, after, update_mask)
    SELECT @project_number, @database_id, s.seq, (@log_keys::bytea[])[w.i], (@log_names::bytea[])[w.i], s.last_ts,
        (@log_befores::bytea[])[w.i], (@log_afters::bytea[])[w.i], (@log_masks::bytea[])[w.i]
    FROM s, generate_subscripts(@log_keys::bytea[], 1) AS w(i)
    RETURNING 1
)
SELECT s.seq, s.last_ts, documents.conflict_unless(
        (SELECT count(*) FROM u) = coalesce(cardinality(@update_keys::bytea[]), 0)
        AND (SELECT count(*) FROM i) = coalesce(cardinality(@insert_keys::bytea[]), 0)
        AND (SELECT count(*) FROM x) = coalesce(cardinality(@delete_keys::bytea[]), 0)
        AND (SELECT count(*) FROM v) = coalesce(cardinality(@replaced_keys::bytea[]), 0)
        AND (SELECT count(*) FROM c) = coalesce(cardinality(@log_keys::bytea[]), 0)) AS ok
FROM s;
