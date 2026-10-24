-- The contract's queries (S2-0). S2-D adds the quota statements here.

-- name: GetUsage :one
SELECT * FROM storage.usage WHERE project_number = @project_number;
