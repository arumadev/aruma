-- Read-only queries of the console's data browser (core/browse.go). They read the stored rows
-- directly, one index probe at a time, so a node with many children is never loaded whole.

-- name: FirstPathIn :one
-- The first row in [lo, hi), in path order.
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
SELECT path FROM rtdb.nodes
WHERE database_id = @database_id AND path >= @lo::text AND path < @hi::text
ORDER BY path LIMIT 1;

-- name: FirstPathFrom :one
-- The first row from lo on, in path order: the root's children have no upper bound.
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
SELECT path FROM rtdb.nodes
WHERE database_id = @database_id AND path >= @lo::text
ORDER BY path LIMIT 1;

-- name: SubtreeSize :one
-- The rows of a subtree and the bytes of their values, counting at most max_rows rows.
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
SELECT count(*)::bigint AS rows, coalesce(sum(octet_length(value::text)), 0)::bigint AS bytes
FROM (
    SELECT value FROM rtdb.nodes
    WHERE database_id = @database_id
      AND (path = @path::text OR (path >= @lo::text AND path < @hi::text))
    LIMIT @max_rows
) s;

-- name: DatabaseSize :one
-- The rows of a whole database and the bytes of their values, counting at most max_rows rows.
-- scope: global — database_id comes from EnsureDatabase, which filters on project_number.
SELECT count(*)::bigint AS rows, coalesce(sum(octet_length(value::text)), 0)::bigint AS bytes
FROM (SELECT value FROM rtdb.nodes WHERE database_id = @database_id LIMIT @max_rows) s;
