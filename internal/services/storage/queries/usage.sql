-- A project's quota account (ADR-022 §6.5). Every reservation lives here, so no ledger in memory
-- disagrees with the database, and Boot reconciles it after a crash.

-- name: GetUsage :one
SELECT * FROM storage.usage WHERE project_number = @project_number;

-- name: EnsureUsage :exec
-- The account is created on first use: an UPDATE on a missing row would match nothing and refuse
-- every write.
INSERT INTO storage.usage (project_number) VALUES (@project_number) ON CONFLICT (project_number) DO NOTHING;

-- name: ReserveUsage :execrows
-- n bytes on their way in, under the quota (0 is unlimited); anonymous_n of them for a session
-- without an identity, under the anonymous cap. No row back is a refusal.
UPDATE storage.usage SET reserved_bytes = reserved_bytes + @n::bigint,
    anonymous_reserved_bytes = anonymous_reserved_bytes + @anonymous_n::bigint
WHERE project_number = @project_number
    AND (@quota::bigint = 0 OR used_bytes + reserved_bytes + @n::bigint <= @quota::bigint)
    AND anonymous_reserved_bytes + @anonymous_n::bigint <= @anonymous_cap::bigint;

-- name: UnreserveUsage :exec
-- Bytes reserved and not stored give their quota back: what a request did not use, a cancel, an
-- expiry, a refusal. Never below zero, and the anonymous part never above the whole.
UPDATE storage.usage SET reserved_bytes = GREATEST(reserved_bytes - @n::bigint, 0),
    anonymous_reserved_bytes = LEAST(GREATEST(anonymous_reserved_bytes - @anonymous_n::bigint, 0),
        GREATEST(reserved_bytes - @n::bigint, 0))
WHERE project_number = @project_number;

-- name: CommitUsage :exec
-- commitObject's move from reserved to used: the new generation's bytes and one more object.
UPDATE storage.usage SET reserved_bytes = GREATEST(reserved_bytes - @n::bigint, 0),
    anonymous_reserved_bytes = LEAST(GREATEST(anonymous_reserved_bytes - @anonymous_n::bigint, 0),
        GREATEST(reserved_bytes - @n::bigint, 0)),
    used_bytes = used_bytes + @size::bigint, object_count = object_count + 1
WHERE project_number = @project_number;

-- name: ReconcileUsage :one
-- scope: global — Boot rebuilds every project's account from the rows before the listener opens
-- (ADR-022 §6.5): used and count from the objects, reserved from the live sessions, so what a
-- crash left reserved goes. It returns the bytes it corrected.
WITH projects AS (
    SELECT project_number FROM storage.usage
    UNION SELECT project_number FROM storage.objects
    UNION SELECT project_number FROM storage.upload_sessions WHERE state = 'active'
), want AS (
    SELECT p.project_number,
        COALESCE((SELECT sum(o.size) FROM storage.objects o WHERE o.project_number = p.project_number), 0)::bigint AS used_bytes,
        (SELECT count(*) FROM storage.objects o WHERE o.project_number = p.project_number)::bigint AS object_count,
        COALESCE((SELECT sum(u.reserved_bytes) FROM storage.upload_sessions u
            WHERE u.project_number = p.project_number AND u.state = 'active'), 0)::bigint AS reserved_bytes,
        COALESCE((SELECT sum(u.reserved_bytes) FROM storage.upload_sessions u
            WHERE u.project_number = p.project_number AND u.state = 'active' AND u.principal_kind = 'anonymous'), 0)::bigint AS anonymous_reserved_bytes
    FROM projects p
), diff AS (
    SELECT COALESCE(sum(abs(w.used_bytes - COALESCE(u.used_bytes, 0)) + abs(w.reserved_bytes - COALESCE(u.reserved_bytes, 0))), 0)::bigint AS corrected
    FROM want w LEFT JOIN storage.usage u ON u.project_number = w.project_number
), written AS (
    INSERT INTO storage.usage (project_number, used_bytes, reserved_bytes, anonymous_reserved_bytes, object_count)
    SELECT project_number, used_bytes, reserved_bytes, anonymous_reserved_bytes, object_count FROM want
    ON CONFLICT (project_number) DO UPDATE SET used_bytes = excluded.used_bytes,
        reserved_bytes = excluded.reserved_bytes, anonymous_reserved_bytes = excluded.anonymous_reserved_bytes,
        object_count = excluded.object_count
    RETURNING 1
)
SELECT diff.corrected, (SELECT count(*) FROM written)::bigint AS projects FROM diff;
