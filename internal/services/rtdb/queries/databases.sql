-- name: EnsureDatabase :one
-- Creates a project's database on first use; a database is never renamed or moved.
WITH created AS (
    INSERT INTO rtdb.databases (project_number, name)
    VALUES (@project_number, @name)
    ON CONFLICT (project_number, name) DO NOTHING
    RETURNING id, seq, rules
)
SELECT id, seq, rules FROM created
UNION ALL
SELECT id, seq, rules FROM rtdb.databases WHERE project_number = @project_number AND name = @name
LIMIT 1;

-- name: AdvanceSeq :one
UPDATE rtdb.databases SET seq = seq + @n::bigint, updated_at = now()
WHERE id = @id AND project_number = @project_number
RETURNING seq;

-- name: GetSeq :one
-- Reads the committed write count, to settle a commit whose outcome is unknown.
SELECT seq FROM rtdb.databases WHERE id = @id AND project_number = @project_number;

-- name: DeleteProjectDatabases :exec
-- A purged project's databases go, their nodes with them (ON DELETE CASCADE).
DELETE FROM rtdb.databases WHERE project_number = @project_number;

-- name: SetRules :exec
-- Rewrites a database's rules document; the database exists (EnsureDatabase).
UPDATE rtdb.databases SET rules = @rules, updated_at = now()
WHERE project_number = @project_number AND name = @name;
