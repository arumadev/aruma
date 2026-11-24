-- Retention (housekeeping): one batch of each table's rows that no feature reads any more. Each
-- predicate is the indexed time of control 00004 and events 00002.

-- name: PurgeEndedSessions :execrows
-- Console sessions a day past their absolute expiry. A session revoked or idle earlier stopped
-- working then (GetActiveSession); the absolute expiry bounds them all, and the console lists
-- only live ones (ListSessions).
DELETE FROM control.sessions
WHERE id IN (
    SELECT s.id FROM control.sessions s
    WHERE s.absolute_expires_at < now() - interval '1 day'
    ORDER BY s.absolute_expires_at
    LIMIT sqlc.arg(batch)::int);

-- name: PurgeEndedAccessTokens :execrows
-- CLI tokens revoked a day ago, and those expired 30 days ago. The console lists expired tokens
-- (ListAccessTokens shows every unrevoked one), so they stay a month for the owner to see why a
-- script stopped; a revoked one is listed nowhere.
DELETE FROM control.access_tokens
WHERE id IN (
    SELECT t.id FROM control.access_tokens t
    WHERE t.revoked_at < now() - interval '1 day'
    LIMIT sqlc.arg(batch)::int)
   OR id IN (
    SELECT t.id FROM control.access_tokens t
    WHERE t.expires_at < now() - interval '30 days'
    LIMIT sqlc.arg(batch)::int);

-- name: PurgeAuditLog :execrows
-- The administrative audit log's rows older than @days days, oldest first. The database refuses
-- to delete a row younger than 7 days (control 00004), so @days must be at least 7.
DELETE FROM control.audit_log
WHERE id IN (
    SELECT a.id FROM control.audit_log a
    WHERE a.at < now() - make_interval(days => sqlc.arg(days)::int)
    ORDER BY a.at
    LIMIT sqlc.arg(batch)::int);

-- name: PurgeEvents :execrows
-- scope: global — housekeeping across projects: the shared change log's entries older than
-- @days days, oldest first.
DELETE FROM events.changes
WHERE seq IN (
    SELECT c.seq FROM events.changes c
    WHERE c.committed_at < now() - make_interval(days => sqlc.arg(days)::int)
    ORDER BY c.committed_at
    LIMIT sqlc.arg(batch)::int);

-- name: PurgeProjectEvents :execrows
-- A purged project's change log, a batch at a time.
DELETE FROM events.changes
WHERE project_number = @project_number::bigint AND seq IN (
    SELECT c.seq FROM events.changes c
    WHERE c.project_number = @project_number::bigint
    ORDER BY c.seq
    LIMIT sqlc.arg(batch)::int);
