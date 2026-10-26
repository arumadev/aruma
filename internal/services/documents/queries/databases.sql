-- The databases of a project and their sequencer rows (ADR-024 §1.2).

-- name: EnsureDatabase :exec
-- Creates a database and its sequencer row together, once: a second call, or a concurrent one,
-- changes nothing. The sequencer starts at seq 0 with the database's create_time.
WITH t AS (
    SELECT clock_timestamp() AS now
), d AS (
    INSERT INTO documents.databases (project_number, database_id, uid, create_time, update_time, etag, location_id)
    SELECT @project_number, @database_id, @uid, t.now, t.now, @etag, @location_id FROM t
    ON CONFLICT DO NOTHING
    RETURNING project_number, database_id, create_time
)
INSERT INTO documents.sequencer (project_number, database_id, last_ts)
SELECT d.project_number, d.database_id, d.create_time FROM d;

-- name: GetDatabase :one
SELECT * FROM documents.databases WHERE project_number = @project_number AND database_id = @database_id;

-- name: ReadHead :one
-- The last commit and the earliest read_time the database serves, in one snapshot.
SELECT q.seq, q.last_ts, (now() - b.version_retention)::timestamptz AS earliest
FROM documents.sequencer q
JOIN documents.databases b ON b.project_number = q.project_number AND b.database_id = q.database_id
WHERE q.project_number = @project_number AND q.database_id = @database_id;

-- name: WaitHead :one
-- Waits for the one commit that may hold the sequencer row between its stamp and COMMIT, then
-- returns the clock: every commit stamped from now on is later (ADR-024 §3).
SELECT clock_timestamp()::timestamptz AS now FROM documents.sequencer
WHERE project_number = @project_number AND database_id = @database_id
FOR SHARE;
