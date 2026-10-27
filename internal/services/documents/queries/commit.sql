-- The commit path (ADR-024 §2.1). core queues these in pgx batches rather than calling the
-- generated methods (core/statements.go): the first batch opens the transaction, sets its
-- timeouts, locks the written documents and reads the catalog; the second writes the index
-- entries, stamps, writes the documents and COMMITs in one round trip, so that only the stamp,
-- the write and COMMIT run while the sequencer row is held. A commit whose documents hold its
-- commit_time splits the second batch after the stamp (StampCommit).

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
-- An index entry the documents' images before the commit had and their images after do not
-- (core/indexentries.go). Like InsertIndexEntries it runs before the stamp, under the documents'
-- row locks, and takes no sequencer row. Each entry is found by its primary key, and the rows
-- found deleted by their ctid: the LATERAL subquery, which its LIMIT keeps from being flattened
-- into a join (an entry is unique by its key, so it drops none), leaves the planner no join to
-- choose. A join was planned from estimates that lag a database grown since its statistics, or
-- that a generic plan guesses: it hashed a scan of every entry of the database for each
-- statement, and a statement of 20,000 entries ran past 100 s (BenchmarkCommitWide).
DELETE FROM documents.index_entries e
WHERE e.ctid = ANY(ARRAY(
    SELECT f.ctid
    FROM (SELECT unnest(@index_ids::bigint[]) AS index_id, unnest(@keys::bytea[]) AS key) x
    CROSS JOIN LATERAL (
        SELECT f.ctid FROM documents.index_entries f
        WHERE f.project_number = @project_number AND f.database_id = @database_id
            AND f.index_id = x.index_id AND f.key = x.key
        LIMIT 1
    ) f
));

-- name: InsertIndexEntries :exec
-- The arrays are unnested, never subscripted: an element of a bytea[] is found by walking the
-- ones before it, so subscripting each made a commit's 200,000 entries take minutes.
INSERT INTO documents.index_entries (project_number, database_id, index_id, key, full_key)
SELECT @project_number, @database_id, x.index_id, x.key, x.full_key
FROM (SELECT unnest(@index_ids::bigint[]) AS index_id, unnest(@keys::bytea[]) AS key,
    unnest(@full_keys::bytea[]) AS full_key) x
ON CONFLICT DO NOTHING;

-- name: StampCommit :many
-- The stamp: the commit's seq and commit_time, taking the sequencer row until COMMIT.
-- clock_timestamp(), never now(): now() is the transaction's start, before the locks were waited
-- for. A catalog changed since the commit computed its index entries fails the statement with
-- 40001 (conflict_unless, checked again on the row it waited for), so the entries queued before
-- it never commit: core rolls back and computes the commit again with the new catalog. A commit
-- whose documents hold its commit_time (a REQUEST_TIME transform) builds them once this
-- returned; any other queues WriteCommit behind it in the same round trip.
UPDATE documents.sequencer q SET seq = q.seq + 1,
    last_ts = greatest(clock_timestamp(), q.last_ts + interval '1 microsecond')
WHERE q.project_number = @project_number AND q.database_id = @database_id
    AND documents.conflict_unless(q.catalog_version = @catalog_version)
RETURNING q.seq, q.last_ts;

-- name: WriteCommit :many
-- The rest of the commit, under the sequencer row StampCommit took, whose seq and commit_time it
-- reads back (the transaction sees its own update): the documents, the images they replace and
-- the changelog. Every write names the update_seq the commit read (inserts expect no row), and
-- conflict_unless fails the statement with 40001 unless each found it (an empty array arrives as
-- NULL, hence the coalesce). After a stamp that found the catalog changed, the version differs
-- here too: no row, and nothing written. Each write's arrays are unnested in one select list
-- (zipped, as they have one length), as no element is subscripted, which walks the elements
-- before it (InsertIndexEntries). The documents an update or delete joins are also named by
-- name_key = ANY(keys), so that the primary key finds them whatever the planner estimates: the
-- documents of a database that grew since its statistics, or of any under a generic plan, were
-- estimated at one row, and the join looped over all of them (generate_subscripts' 1000 rows
-- had made it hash a scan of the whole table).
WITH s AS (
    SELECT q.seq, q.last_ts FROM documents.sequencer q
    WHERE q.project_number = @project_number AND q.database_id = @database_id
        AND q.catalog_version = @catalog_version
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
    UPDATE documents.documents d SET doc = w.doc, size = w.size, update_time = s.last_ts, update_seq = s.seq
    FROM s, (SELECT unnest(@update_keys::bytea[]) AS name_key, unnest(@update_seqs::bigint[]) AS update_seq,
        unnest(@update_docs::bytea[]) AS doc, unnest(@update_sizes::integer[]) AS size) w
    WHERE d.project_number = @project_number AND d.database_id = @database_id
        AND d.name_key = ANY(@update_keys::bytea[]) AND d.name_key = w.name_key AND d.update_seq = w.update_seq
    RETURNING 1
), i AS (
    INSERT INTO documents.documents (project_number, database_id, name_key, name, parent_hash, cg_hash,
        parent, collection_id, doc, create_time, update_time, update_seq, size)
    SELECT @project_number, @database_id, w.name_key, w.name, w.parent_hash, w.cg_hash, w.parent,
        w.collection_id, w.doc, s.last_ts, s.last_ts, s.seq, w.size
    FROM s, (SELECT unnest(@insert_keys::bytea[]) AS name_key, unnest(@insert_names::bytea[]) AS name,
        unnest(@insert_parent_hashes::bytea[]) AS parent_hash, unnest(@insert_cg_hashes::bytea[]) AS cg_hash,
        unnest(@insert_parents::bytea[]) AS parent, unnest(@insert_collection_ids::text[]) AS collection_id,
        unnest(@insert_docs::bytea[]) AS doc, unnest(@insert_sizes::integer[]) AS size) w
    ON CONFLICT DO NOTHING
    RETURNING 1
), x AS (
    DELETE FROM documents.documents d
    USING s, (SELECT unnest(@delete_keys::bytea[]) AS name_key, unnest(@delete_seqs::bigint[]) AS update_seq) w
    WHERE d.project_number = @project_number AND d.database_id = @database_id
        AND d.name_key = ANY(@delete_keys::bytea[]) AND d.name_key = w.name_key AND d.update_seq = w.update_seq
    RETURNING 1
), c AS (
    INSERT INTO documents.changelog (project_number, database_id, seq, name_key, name, commit_time,
        before, after, update_mask)
    SELECT @project_number, @database_id, s.seq, w.name_key, w.name, s.last_ts, w.before, w.after, w.update_mask
    FROM s, (SELECT unnest(@log_keys::bytea[]) AS name_key, unnest(@log_names::bytea[]) AS name,
        unnest(@log_befores::bytea[]) AS before, unnest(@log_afters::bytea[]) AS after,
        unnest(@log_masks::bytea[]) AS update_mask) w
    RETURNING 1
)
SELECT documents.conflict_unless(
        (SELECT count(*) FROM u) = coalesce(cardinality(@update_keys::bytea[]), 0)
        AND (SELECT count(*) FROM i) = coalesce(cardinality(@insert_keys::bytea[]), 0)
        AND (SELECT count(*) FROM x) = coalesce(cardinality(@delete_keys::bytea[]), 0)
        AND (SELECT count(*) FROM v) = coalesce(cardinality(@replaced_keys::bytea[]), 0)
        AND (SELECT count(*) FROM c) = coalesce(cardinality(@log_keys::bytea[]), 0)) AS ok
FROM s;

-- name: UnchangedCommitTime :one
-- The commit_time of a commit that changes nothing: the clock, as production's advances for one
-- (f1-write-results.json rest.update_unchanged, rest.verify_existing), and at or after the last
-- commit, so at or after every update_time the commit read. It takes no seq: a read at it waits
-- for the commit that may be between its stamp and COMMIT (WaitHead), as at any time past last_ts.
SELECT greatest(clock_timestamp(), last_ts)::timestamptz AS commit_time FROM documents.sequencer
WHERE project_number = @project_number AND database_id = @database_id;
