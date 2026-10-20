-- name: EnsureDatabase :one
-- Creates a project's database on first use; a database is never renamed or moved.
WITH created AS (
    INSERT INTO rtdb.databases (project_number, name)
    VALUES (@project_number, @name)
    ON CONFLICT (project_number, name) DO NOTHING
    RETURNING id, seq
)
SELECT id, seq FROM created
UNION ALL
SELECT id, seq FROM rtdb.databases WHERE project_number = @project_number AND name = @name
LIMIT 1;

-- name: AdvanceSeq :one
UPDATE rtdb.databases SET seq = seq + @n::bigint, updated_at = now()
WHERE id = @id AND project_number = @project_number
RETURNING seq;
