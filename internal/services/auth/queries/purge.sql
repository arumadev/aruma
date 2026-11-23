-- A purged project's Auth data (tenancy's OnProjectPurged, once the project's deletion has
-- expired). Every statement names the project; the large tables go a batch at a time, and the
-- purge runs again at each housekeeping pass until nothing is left. Refresh tokens go before
-- their users, so the users' delete trigger has none left to mark.

-- name: PurgeProjectRefreshTokens :execrows
DELETE FROM auth.refresh_tokens
WHERE project_number = @project_number::bigint AND token_hash IN (
    SELECT r.token_hash FROM auth.refresh_tokens r
    WHERE r.project_number = @project_number::bigint
    LIMIT sqlc.arg(batch)::int);

-- name: PurgeProjectUsers :execrows
-- Their passwords, providers and Apple tokens go with them (ON DELETE CASCADE).
DELETE FROM auth.users
WHERE project_number = @project_number::bigint AND (tenant_id, uid) IN (
    SELECT u.tenant_id, u.uid FROM auth.users u
    WHERE u.project_number = @project_number::bigint
    LIMIT sqlc.arg(batch)::int);

-- name: PurgeProjectAuthAudit :execrows
DELETE FROM auth.audit
WHERE project_number = @project_number::bigint AND id IN (
    SELECT a.id FROM auth.audit a
    WHERE a.project_number = @project_number::bigint
    LIMIT sqlc.arg(batch)::int);

-- name: PurgeProjectOutbox :execrows
DELETE FROM auth.email_outbox
WHERE project_number = @project_number::bigint AND id IN (
    SELECT m.id FROM auth.email_outbox m
    WHERE m.project_number = @project_number::bigint
    LIMIT sqlc.arg(batch)::int);

-- name: PurgeProjectOobCodes :exec
DELETE FROM auth.oob_codes WHERE project_number = @project_number::bigint;

-- name: PurgeProjectIdPSessions :exec
DELETE FROM auth.idp_sessions WHERE project_number = @project_number::bigint;

-- name: PurgeProjectIdPConfigs :exec
DELETE FROM auth.idp_configs WHERE project_number = @project_number::bigint;

-- name: PurgeProjectSigningKeys :exec
DELETE FROM auth.signing_keys WHERE project_number = @project_number::bigint;

-- name: PurgeProjectEmailTemplates :exec
DELETE FROM auth.email_templates WHERE project_number = @project_number::bigint;

-- name: PurgeProjectAuthConfig :exec
DELETE FROM auth.project_config WHERE project_number = @project_number::bigint;
