-- Document reads (ADR-024 §1.2, §3). Every read is one statement, so it sees one snapshot: the
-- latest reads return the sequencer's seq and last_ts of that snapshot beside the rows, and
-- last_ts is the read_time they answer at. The reads at a read_time T take a document's row if
-- its update_time <= T, else its versions row with begin_time <= T < end_time.
--
-- Keys are cut (migration documents/00001): a bound is cut to its stored form and the whole name,
-- name, filters exactly; core orders rows that share a cut prefix by name.

-- name: GetDocuments :many
SELECT s.seq, s.last_ts, d.name, d.doc, d.create_time, d.update_time
FROM documents.sequencer s
LEFT JOIN documents.documents d ON d.project_number = s.project_number AND d.database_id = s.database_id
    AND d.name_key = ANY(@name_keys::bytea[])
WHERE s.project_number = @project_number AND s.database_id = @database_id;

-- name: GetDocumentsAt :many
SELECT s.seq, s.last_ts, (now() - b.version_retention)::timestamptz AS earliest,
    x.name, x.doc, x.create_time, x.update_time
FROM documents.sequencer s
JOIN documents.databases b ON b.project_number = s.project_number AND b.database_id = s.database_id
LEFT JOIN LATERAL (
    SELECT d.name, d.doc, d.create_time, d.update_time
    FROM documents.documents d
    WHERE d.project_number = s.project_number AND d.database_id = s.database_id
        AND d.name_key = ANY(@name_keys::bytea[]) AND d.update_time <= @read_time::timestamptz
    UNION ALL
    SELECT v.name, v.doc, v.create_time, v.begin_time
    FROM documents.versions v
    WHERE v.project_number = s.project_number AND v.database_id = s.database_id
        AND v.name_key = ANY(@name_keys::bytea[])
        AND v.begin_time <= @read_time::timestamptz AND v.end_time > @read_time::timestamptz
) x ON true
WHERE s.project_number = @project_number AND s.database_id = @database_id;

-- name: ListCollection :many
-- A collection's documents from name key lo on, whose whole name is above after.
SELECT s.seq, s.last_ts, x.name, x.doc, x.create_time, x.update_time
FROM documents.sequencer s
LEFT JOIN LATERAL (
    SELECT d.name_key, d.name, d.doc, d.create_time, d.update_time
    FROM documents.documents d
    WHERE d.project_number = s.project_number AND d.database_id = s.database_id
        AND d.parent_hash = @parent_hash AND d.parent = @parent AND d.collection_id = @collection_id
        AND d.name_key >= @lo AND d.name > @after
    ORDER BY d.name_key
    LIMIT @row_limit
) x ON true
WHERE s.project_number = @project_number AND s.database_id = @database_id
ORDER BY x.name_key;

-- name: ListCollectionAt :many
SELECT s.seq, s.last_ts, (now() - b.version_retention)::timestamptz AS earliest,
    x.name, x.doc, x.create_time, x.update_time
FROM documents.sequencer s
JOIN documents.databases b ON b.project_number = s.project_number AND b.database_id = s.database_id
LEFT JOIN LATERAL (
    SELECT y.name_key, y.name, y.doc, y.create_time, y.update_time FROM (
        SELECT d.name_key, d.name, d.doc, d.create_time, d.update_time
        FROM documents.documents d
        WHERE d.project_number = s.project_number AND d.database_id = s.database_id
            AND d.parent_hash = @parent_hash AND d.parent = @parent AND d.collection_id = @collection_id
            AND d.name_key >= @lo AND d.name > @after AND d.update_time <= @read_time::timestamptz
        UNION ALL
        SELECT v.name_key, v.name, v.doc, v.create_time, v.begin_time
        FROM documents.versions v
        WHERE v.project_number = s.project_number AND v.database_id = s.database_id
            AND v.parent_hash = @parent_hash AND v.parent = @parent AND v.collection_id = @collection_id
            AND v.name_key >= @lo AND v.name > @after
            AND v.begin_time <= @read_time::timestamptz AND v.end_time > @read_time::timestamptz
    ) y
    ORDER BY y.name_key
    LIMIT @row_limit
) x ON true
WHERE s.project_number = @project_number AND s.database_id = @database_id
ORDER BY x.name_key;

-- name: RangeAt :many
-- Every document at T with a name key in [lo, hi]: a run of keys that share their cut prefix,
-- which core orders by name.
SELECT d.name, d.doc, d.create_time, d.update_time
FROM documents.documents d
WHERE d.project_number = @project_number AND d.database_id = @database_id
    AND d.name_key BETWEEN @lo AND @hi AND d.update_time <= @read_time::timestamptz
UNION ALL
SELECT v.name, v.doc, v.create_time, v.begin_time
FROM documents.versions v
WHERE v.project_number = @project_number AND v.database_id = @database_id
    AND v.name_key BETWEEN @lo AND @hi
    AND v.begin_time <= @read_time::timestamptz AND v.end_time > @read_time::timestamptz;

-- name: WalkChildren :many
-- The children of a key prefix of plen bytes, one row per child, by a skipping scan: from the
-- first row in [lo, hi) whose whole name is at least after, each step finds the first row past
-- the previous child's subtree (documents.child_skip). A child is a document under a collection
-- prefix (show_missing) or a collection under a document's (ListCollectionIds); its row is the
-- document itself when that exists, else any row below it. At most max_steps children.
WITH RECURSIVE s AS (
    SELECT q.seq, q.last_ts FROM documents.sequencer q
    WHERE q.project_number = @project_number AND q.database_id = @database_id
), walk AS (
    (SELECT d.name_key, d.name, d.doc, d.create_time, d.update_time, 1 AS step
     FROM documents.documents d
     WHERE d.project_number = @project_number AND d.database_id = @database_id
         AND d.name_key >= @lo AND d.name_key < @hi AND d.name >= @after
     ORDER BY d.name_key
     LIMIT 1)
    UNION ALL
    SELECT n.name_key, n.name, n.doc, n.create_time, n.update_time, w.step + 1
    FROM walk w
    CROSS JOIN LATERAL (
        SELECT d.name_key, d.name, d.doc, d.create_time, d.update_time
        FROM documents.documents d
        WHERE d.project_number = @project_number AND d.database_id = @database_id
            AND d.name_key >= documents.child_skip(w.name, @plen::integer) AND d.name_key < @hi
        ORDER BY d.name_key
        LIMIT 1
    ) n
    WHERE w.step < @max_steps::integer
)
SELECT s.seq, s.last_ts, walk.name, walk.doc, walk.create_time, walk.update_time
FROM s LEFT JOIN walk ON true
ORDER BY walk.step;

-- name: WalkChildrenAt :many
WITH RECURSIVE s AS (
    SELECT q.seq, q.last_ts, (now() - b.version_retention)::timestamptz AS earliest
    FROM documents.sequencer q
    JOIN documents.databases b ON b.project_number = q.project_number AND b.database_id = q.database_id
    WHERE q.project_number = @project_number AND q.database_id = @database_id
), walk AS (
    (SELECT y.name_key, y.name, y.doc, y.create_time, y.update_time, 1 AS step FROM (
        SELECT d.name_key, d.name, d.doc, d.create_time, d.update_time
        FROM documents.documents d
        WHERE d.project_number = @project_number AND d.database_id = @database_id
            AND d.name_key >= @lo AND d.name_key < @hi AND d.name >= @after
            AND d.update_time <= @read_time::timestamptz
        UNION ALL
        SELECT v.name_key, v.name, v.doc, v.create_time, v.begin_time
        FROM documents.versions v
        WHERE v.project_number = @project_number AND v.database_id = @database_id
            AND v.name_key >= @lo AND v.name_key < @hi AND v.name >= @after
            AND v.begin_time <= @read_time::timestamptz AND v.end_time > @read_time::timestamptz
    ) y
    ORDER BY y.name_key
    LIMIT 1)
    UNION ALL
    SELECT n.name_key, n.name, n.doc, n.create_time, n.update_time, w.step + 1
    FROM walk w
    CROSS JOIN LATERAL (
        SELECT y.name_key, y.name, y.doc, y.create_time, y.update_time FROM (
            SELECT d.name_key, d.name, d.doc, d.create_time, d.update_time
            FROM documents.documents d
            WHERE d.project_number = @project_number AND d.database_id = @database_id
                AND d.name_key >= documents.child_skip(w.name, @plen::integer) AND d.name_key < @hi
                AND d.update_time <= @read_time::timestamptz
            UNION ALL
            SELECT v.name_key, v.name, v.doc, v.create_time, v.begin_time
            FROM documents.versions v
            WHERE v.project_number = @project_number AND v.database_id = @database_id
                AND v.name_key >= documents.child_skip(w.name, @plen::integer) AND v.name_key < @hi
                AND v.begin_time <= @read_time::timestamptz AND v.end_time > @read_time::timestamptz
        ) y
        ORDER BY y.name_key
        LIMIT 1
    ) n
    WHERE w.step < @max_steps::integer
)
SELECT s.seq, s.last_ts, s.earliest, walk.name, walk.doc, walk.create_time, walk.update_time
FROM s LEFT JOIN walk ON true
ORDER BY walk.step;
