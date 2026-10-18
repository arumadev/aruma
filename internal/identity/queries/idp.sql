-- name: GetIdPConfig :one
SELECT * FROM auth.idp_configs WHERE project_number = @project_number AND provider_id = @provider_id;

-- name: ListIdPConfigs :many
SELECT * FROM auth.idp_configs WHERE project_number = @project_number ORDER BY provider_id;

-- name: UpsertIdPConfig :one
INSERT INTO auth.idp_configs (project_number, provider_id, enabled, client_id, client_secret_enc, apple_team_id, apple_key_id, apple_private_key_enc)
VALUES (@project_number, @provider_id, @enabled, @client_id, sqlc.narg(client_secret_enc), @apple_team_id, @apple_key_id, sqlc.narg(apple_private_key_enc))
ON CONFLICT (project_number, provider_id) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    client_id = EXCLUDED.client_id,
    client_secret_enc = EXCLUDED.client_secret_enc,
    apple_team_id = EXCLUDED.apple_team_id,
    apple_key_id = EXCLUDED.apple_key_id,
    apple_private_key_enc = EXCLUDED.apple_private_key_enc,
    updated_at = now()
RETURNING *;

-- name: CreateIdPSession :exec
INSERT INTO auth.idp_sessions (session_hash, state_hash, project_number, tenant_id, provider_id, nonce, redirect_url,
    callback_url, code_verifier_enc, event_id, auth_type, app_name, api_key, expires_at)
VALUES (@session_hash, @state_hash, @project_number, @tenant_id, @provider_id, @nonce, @redirect_url,
    @callback_url, sqlc.narg(code_verifier_enc), sqlc.narg(event_id), @auth_type, @app_name, @api_key, @expires_at);

-- name: FindIdPSessionByState :one
-- scope: global — the provider's callback carries only the state; the row names its project.
SELECT * FROM auth.idp_sessions
WHERE state_hash = @state_hash AND used_at IS NULL AND expires_at > now();

-- name: UseIdPSession :one
-- Marks the session used, once: a replayed state finds no row.
UPDATE auth.idp_sessions SET used_at = now()
WHERE project_number = @project_number AND state_hash = @state_hash AND used_at IS NULL AND expires_at > now()
RETURNING *;

-- name: PurgeIdPSessions :execrows
-- scope: global — housekeeping across projects.
DELETE FROM auth.idp_sessions WHERE expires_at < now() - interval '1 day';

-- name: UpsertIdPToken :exec
INSERT INTO auth.idp_tokens (project_number, tenant_id, uid, provider_id, refresh_token_enc)
VALUES (@project_number, @tenant_id, @uid, @provider_id, @refresh_token_enc)
ON CONFLICT (project_number, tenant_id, uid, provider_id) DO UPDATE
SET refresh_token_enc = EXCLUDED.refresh_token_enc, updated_at = now();

-- name: GetIdPToken :one
SELECT * FROM auth.idp_tokens
WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = @uid AND provider_id = @provider_id;
