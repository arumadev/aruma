-- Rows of rtdb.nodes are keyed by database_id, a key unique across projects that only
-- EnsureDatabase hands out, filtering on project_number. A subtree at path p is p's own row and
-- the range [p || '/', p || '0'); the caller computes the bounds, and the root reads all rows.

-- name: ReadSubtree :many
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
SELECT path, value, priority FROM rtdb.nodes
WHERE database_id = @database_id
  AND (path = @path::text OR (path >= @lo::text AND path < @hi::text));

-- name: ReadAll :many
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
SELECT path, value, priority FROM rtdb.nodes WHERE database_id = @database_id;

-- name: ReadRows :many
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
SELECT path, value, priority FROM rtdb.nodes
WHERE database_id = @database_id AND path = ANY (@paths::text[]);

-- name: HasRowsIn :one
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
SELECT EXISTS (
    SELECT 1 FROM rtdb.nodes
    WHERE database_id = @database_id AND path >= @lo::text AND path < @hi::text
);

-- name: HasRowsOutside :one
-- Whether a row lies below lo, and before hi when bounded, under a child whose key is not in
-- keys: the part of a path from its character skip, 1-based, up to the next slash, is that key. For
-- security rules, which ask whether a node survives a write that deletes some of its children.
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
SELECT EXISTS (
    SELECT 1 FROM rtdb.nodes
    WHERE database_id = @database_id AND path > @lo::text AND (path < @hi::text OR NOT @bounded::boolean)
      AND NOT (split_part(substr(path, @skip::integer), '/', 1) = ANY (@keys::text[]))
);

-- name: HasRowsBelowRoot :one
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
SELECT EXISTS (SELECT 1 FROM rtdb.nodes WHERE database_id = @database_id AND path <> '');

-- name: DeleteSubtree :exec
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
DELETE FROM rtdb.nodes
WHERE database_id = @database_id
  AND (path = @path::text OR (path >= @lo::text AND path < @hi::text));

-- name: DeleteAll :exec
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
DELETE FROM rtdb.nodes WHERE database_id = @database_id;

-- name: DeleteRow :exec
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
DELETE FROM rtdb.nodes WHERE database_id = @database_id AND path = @path;

-- name: InsertNodes :copyfrom
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
INSERT INTO rtdb.nodes (database_id, path, value, priority) VALUES (@database_id, @path, @value, @priority);

-- name: SetPriority :exec
-- Sets the priority on the row at path: a leaf keeps its value; an object gets a priority row.
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
INSERT INTO rtdb.nodes (database_id, path, value, priority)
VALUES (@database_id, @path, NULL, @priority::jsonb)
ON CONFLICT (database_id, path) DO UPDATE SET priority = EXCLUDED.priority;

-- name: UpdateRow :exec
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
UPDATE rtdb.nodes SET value = @value, priority = @priority
WHERE database_id = @database_id AND path = @path;
