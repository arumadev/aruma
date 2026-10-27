-- The reference path's scans (ADR-025 §1): the documents below a parent, of every collection
-- (a kindless all-descendants query) or of one collection id (a collection group query), in
-- name key order, at the latest or at a read_time as documents.sql's reads are. lo and hi
-- bound the stored name_key, after the whole name; core orders rows that share a cut prefix by
-- name and drops those outside the parent by the whole name.

-- name: ScanRange :many
SELECT s.seq, s.last_ts, x.name, x.doc, x.create_time, x.update_time
FROM documents.sequencer s
LEFT JOIN LATERAL (
    SELECT d.name_key, d.name, d.doc, d.create_time, d.update_time
    FROM documents.documents d
    WHERE d.project_number = s.project_number AND d.database_id = s.database_id
        AND d.name_key >= @lo AND d.name_key < @hi AND d.name > @after
    ORDER BY d.name_key
    LIMIT @row_limit
) x ON true
WHERE s.project_number = @project_number AND s.database_id = @database_id
ORDER BY x.name_key;

-- name: ScanRangeAt :many
SELECT s.seq, s.last_ts, (now() - b.version_retention)::timestamptz AS earliest,
    x.name, x.doc, x.create_time, x.update_time
FROM documents.sequencer s
JOIN documents.databases b ON b.project_number = s.project_number AND b.database_id = s.database_id
LEFT JOIN LATERAL (
    SELECT y.name_key, y.name, y.doc, y.create_time, y.update_time FROM (
        SELECT d.name_key, d.name, d.doc, d.create_time, d.update_time
        FROM documents.documents d
        WHERE d.project_number = s.project_number AND d.database_id = s.database_id
            AND d.name_key >= @lo AND d.name_key < @hi AND d.name > @after
            AND d.update_time <= @read_time::timestamptz
        UNION ALL
        SELECT v.name_key, v.name, v.doc, v.create_time, v.begin_time
        FROM documents.versions v
        WHERE v.project_number = s.project_number AND v.database_id = s.database_id
            AND v.name_key >= @lo AND v.name_key < @hi AND v.name > @after
            AND v.begin_time <= @read_time::timestamptz AND v.end_time > @read_time::timestamptz
    ) y
    ORDER BY y.name_key
    LIMIT @row_limit
) x ON true
WHERE s.project_number = @project_number AND s.database_id = @database_id
ORDER BY x.name_key;

-- name: ScanGroup :many
SELECT s.seq, s.last_ts, x.name, x.doc, x.create_time, x.update_time
FROM documents.sequencer s
LEFT JOIN LATERAL (
    SELECT d.name_key, d.name, d.doc, d.create_time, d.update_time
    FROM documents.documents d
    WHERE d.project_number = s.project_number AND d.database_id = s.database_id
        AND d.cg_hash = @cg_hash AND d.collection_id = @collection_id
        AND d.name_key >= @lo AND d.name_key < @hi AND d.name > @after
    ORDER BY d.name_key
    LIMIT @row_limit
) x ON true
WHERE s.project_number = @project_number AND s.database_id = @database_id
ORDER BY x.name_key;

-- name: ScanGroupAt :many
SELECT s.seq, s.last_ts, (now() - b.version_retention)::timestamptz AS earliest,
    x.name, x.doc, x.create_time, x.update_time
FROM documents.sequencer s
JOIN documents.databases b ON b.project_number = s.project_number AND b.database_id = s.database_id
LEFT JOIN LATERAL (
    SELECT y.name_key, y.name, y.doc, y.create_time, y.update_time FROM (
        SELECT d.name_key, d.name, d.doc, d.create_time, d.update_time
        FROM documents.documents d
        WHERE d.project_number = s.project_number AND d.database_id = s.database_id
            AND d.cg_hash = @cg_hash AND d.collection_id = @collection_id
            AND d.name_key >= @lo AND d.name_key < @hi AND d.name > @after
            AND d.update_time <= @read_time::timestamptz
        UNION ALL
        SELECT v.name_key, v.name, v.doc, v.create_time, v.begin_time
        FROM documents.versions v
        WHERE v.project_number = s.project_number AND v.database_id = s.database_id
            AND v.cg_hash = @cg_hash AND v.collection_id = @collection_id
            AND v.name_key >= @lo AND v.name_key < @hi AND v.name > @after
            AND v.begin_time <= @read_time::timestamptz AND v.end_time > @read_time::timestamptz
    ) y
    ORDER BY y.name_key
    LIMIT @row_limit
) x ON true
WHERE s.project_number = @project_number AND s.database_id = @database_id
ORDER BY x.name_key;
