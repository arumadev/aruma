-- name: CreateRefreshToken :one
-- Starts a session no earlier than the user's revocation point, which RevokeUserTokens puts at
-- the next whole second: a session the revoking transaction itself starts (the new session of a
-- password change) is dated there and stays valid. Returns the session's auth_time.
INSERT INTO auth.refresh_tokens (token_hash, project_number, tenant_id, uid, sign_in_provider, custom_claims, auth_time, created_at)
SELECT @token_hash, u.project_number, u.tenant_id, u.uid, @sign_in_provider, sqlc.narg(custom_claims),
       GREATEST(now(), u.tokens_valid_after), GREATEST(now(), u.tokens_valid_after)
FROM auth.users u
WHERE u.project_number = @project_number AND u.tenant_id = @tenant_id AND u.uid = @uid
RETURNING auth_time;

-- name: GetRefreshToken :one
SELECT * FROM auth.refresh_tokens WHERE project_number = @project_number AND token_hash = @token_hash;

-- name: DeleteRefreshToken :exec
DELETE FROM auth.refresh_tokens WHERE project_number = @project_number AND token_hash = @token_hash;

-- name: PurgeOrphanRefreshTokens :execrows
-- scope: global — housekeeping across projects: tokens of deleted users, or revoked long ago.
DELETE FROM auth.refresh_tokens r
WHERE r.created_at < now() - interval '30 days'
  AND NOT EXISTS (SELECT 1 FROM auth.users u
                  WHERE u.project_number = r.project_number AND u.tenant_id = r.tenant_id
                    AND u.uid = r.uid AND u.tokens_valid_after <= r.created_at);

-- name: TouchRefreshToken :exec
UPDATE auth.refresh_tokens SET last_used_at = now() WHERE project_number = @project_number AND token_hash = @token_hash;

-- name: InsertSigningKey :one
INSERT INTO auth.signing_keys (kid, project_number, purpose, state, private_key_enc, public_jwk, activated_at)
VALUES (@kid, @project_number, @purpose, @state, @private_key_enc, @public_jwk, sqlc.narg(activated_at))
RETURNING *;

-- name: ListSigningKeys :many
SELECT * FROM auth.signing_keys
WHERE project_number = @project_number AND purpose = @purpose AND state <> 'destroyed'
ORDER BY created_at;

-- name: GetSigningKey :one
SELECT * FROM auth.signing_keys WHERE project_number = @project_number AND kid = @kid;

-- name: SetSigningKeyState :exec
UPDATE auth.signing_keys SET
    state = @state,
    activated_at = CASE WHEN @state = 'active' THEN now() ELSE activated_at END,
    retired_at = CASE WHEN @state = 'retired' THEN now() ELSE retired_at END,
    private_key_enc = CASE WHEN @state = 'destroyed' THEN NULL ELSE private_key_enc END
WHERE project_number = @project_number AND kid = @kid;

-- name: ListRotationDue :many
-- scope: global — the rotation job walks every project's active keys.
SELECT project_number, purpose, kid, activated_at FROM auth.signing_keys
WHERE state = 'active' AND activated_at < @before;

-- name: ListRetiredToDestroy :many
-- scope: global — the rotation job destroys retired keys whose tokens have all expired.
SELECT project_number, kid FROM auth.signing_keys
WHERE state = 'retired' AND retired_at < @before AND purpose = @purpose;

-- name: InsertAuthAudit :exec
INSERT INTO auth.audit (project_number, tenant_id, uid, event, result, ip, details)
VALUES (@project_number, @tenant_id, sqlc.narg(uid), @event, @result, sqlc.narg(ip), @details);
