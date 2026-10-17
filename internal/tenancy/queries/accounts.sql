-- name: CountAccounts :one
SELECT count(*) FROM control.accounts;

-- name: CreateAccount :one
INSERT INTO control.accounts (email, password_hash, display_name, locale)
VALUES (@email, @password_hash, @display_name, @locale)
RETURNING *;

-- name: GetAccount :one
SELECT * FROM control.accounts WHERE id = @id;

-- name: GetAccountByEmail :one
SELECT * FROM control.accounts WHERE email_lower = lower(@email::text);

-- name: UpdateAccountProfile :one
UPDATE control.accounts
SET display_name = coalesce(sqlc.narg(display_name), display_name),
    locale = coalesce(sqlc.narg(locale), locale),
    updated_at = now()
WHERE id = @id
RETURNING *;

-- name: SetAccountPassword :exec
UPDATE control.accounts SET password_hash = @password_hash, updated_at = now() WHERE id = @id;

-- name: SetAccountDisabled :exec
UPDATE control.accounts SET disabled = @disabled, updated_at = now() WHERE id = @id;

-- name: CreateSession :one
INSERT INTO control.sessions (account_id, token_hash, ip, user_agent, idle_expires_at, absolute_expires_at)
VALUES (@account_id, @token_hash, @ip, @user_agent, @idle_expires_at, @absolute_expires_at)
RETURNING *;

-- name: GetActiveSession :one
-- A session is valid while unrevoked, within both lifetimes, and its account is enabled.
SELECT sqlc.embed(s), a.disabled AS account_disabled
FROM control.sessions s
JOIN control.accounts a ON a.id = s.account_id
WHERE s.token_hash = @token_hash
  AND s.revoked_at IS NULL
  AND s.idle_expires_at > now()
  AND s.absolute_expires_at > now();

-- name: TouchSession :exec
UPDATE control.sessions
SET last_seen_at = now(), idle_expires_at = least(@idle_expires_at, absolute_expires_at)
WHERE id = @id;

-- name: ListSessions :many
SELECT * FROM control.sessions
WHERE account_id = @account_id AND revoked_at IS NULL
  AND idle_expires_at > now() AND absolute_expires_at > now()
ORDER BY last_seen_at DESC;

-- name: RevokeSession :execrows
UPDATE control.sessions SET revoked_at = now()
WHERE id = @id AND account_id = @account_id AND revoked_at IS NULL;

-- name: RevokeOtherSessions :exec
UPDATE control.sessions SET revoked_at = now()
WHERE account_id = @account_id AND revoked_at IS NULL AND id <> @keep_id;

-- name: RevokeAllSessions :exec
UPDATE control.sessions SET revoked_at = now()
WHERE account_id = @account_id AND revoked_at IS NULL;

-- name: CreateAccessToken :one
INSERT INTO control.access_tokens (account_id, name, prefix, token_hash, expires_at)
VALUES (@account_id, @name, @prefix, @token_hash, @expires_at)
RETURNING *;

-- name: GetActiveAccessToken :one
SELECT sqlc.embed(t), a.disabled AS account_disabled
FROM control.access_tokens t
JOIN control.accounts a ON a.id = t.account_id
WHERE t.token_hash = @token_hash
  AND t.revoked_at IS NULL
  AND (t.expires_at IS NULL OR t.expires_at > now());

-- name: TouchAccessToken :exec
UPDATE control.access_tokens SET last_used_at = now() WHERE id = @id;

-- name: ListAccessTokens :many
SELECT * FROM control.access_tokens
WHERE account_id = @account_id AND revoked_at IS NULL
ORDER BY created_at DESC;

-- name: RevokeAccessToken :execrows
UPDATE control.access_tokens SET revoked_at = now()
WHERE id = @id AND account_id = @account_id AND revoked_at IS NULL;

-- name: RevokeAllAccessTokens :exec
UPDATE control.access_tokens SET revoked_at = now()
WHERE account_id = @account_id AND revoked_at IS NULL;
