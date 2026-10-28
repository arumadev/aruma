-- The dashboard's usage counts, an hour a row (core/usage.go; ADR-030 §8).

-- name: AddUsageHours :exec
-- scope: global — one flush adds the counts of every project the process counted, a row each.
-- Counts add to the row's; peaks keep the greater.
INSERT INTO documents.usage_hours AS u (project_number, hour, reads, writes, deletes,
    listeners_peak, connections_peak, rules_allows, rules_denies, rules_errors)
SELECT unnest(@project_numbers::bigint[]), unnest(@hours::timestamptz[]), unnest(@reads::bigint[]),
    unnest(@writes::bigint[]), unnest(@deletes::bigint[]), unnest(@listeners_peaks::bigint[]),
    unnest(@connections_peaks::bigint[]), unnest(@rules_allows::bigint[]), unnest(@rules_denies::bigint[]),
    unnest(@rules_errors::bigint[])
ON CONFLICT (project_number, hour) DO UPDATE SET
    reads = u.reads + excluded.reads,
    writes = u.writes + excluded.writes,
    deletes = u.deletes + excluded.deletes,
    listeners_peak = greatest(u.listeners_peak, excluded.listeners_peak),
    connections_peak = greatest(u.connections_peak, excluded.connections_peak),
    rules_allows = u.rules_allows + excluded.rules_allows,
    rules_denies = u.rules_denies + excluded.rules_denies,
    rules_errors = u.rules_errors + excluded.rules_errors;

-- name: ListUsageHours :many
-- A project's hours from @since on, oldest first.
SELECT hour, reads, writes, deletes, listeners_peak, connections_peak, rules_allows, rules_denies, rules_errors
FROM documents.usage_hours
WHERE project_number = @project_number AND hour >= @since
ORDER BY hour;

-- name: PruneUsageHours :execrows
-- scope: global — retention drops every project's hours before @before.
DELETE FROM documents.usage_hours WHERE hour < @before;

-- name: PurgeUsageHours :exec
DELETE FROM documents.usage_hours WHERE project_number = @project_number;
