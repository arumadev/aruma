-- name: EnsureProjectConfig :exec
INSERT INTO auth.project_config (project_number) VALUES (@project_number) ON CONFLICT DO NOTHING;

-- name: GetProjectConfig :one
SELECT * FROM auth.project_config WHERE project_number = @project_number;

-- name: UpdateProjectConfig :one
UPDATE auth.project_config SET
    password_enabled = @password_enabled,
    anonymous_enabled = @anonymous_enabled,
    client_signup_enabled = @client_signup_enabled,
    enumeration_protection = @enumeration_protection,
    authorized_domains = @authorized_domains,
    password_min_length = @password_min_length,
    password_require_lower = @password_require_lower,
    password_require_upper = @password_require_upper,
    password_require_digit = @password_require_digit,
    password_require_symbol = @password_require_symbol,
    password_enforcement = @password_enforcement,
    app_name = @app_name,
    updated_at = now()
WHERE project_number = @project_number
RETURNING *;

-- name: CreateUser :one
INSERT INTO auth.users (project_number, tenant_id, uid, email, email_verified, display_name, photo_url, disabled, custom_claims, last_login_at)
VALUES (@project_number, @tenant_id, @uid, sqlc.narg(email), @email_verified, sqlc.narg(display_name), sqlc.narg(photo_url), @disabled, sqlc.narg(custom_claims), sqlc.narg(last_login_at))
RETURNING *;

-- name: GetUser :one
SELECT * FROM auth.users WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = @uid;

-- name: GetUserForUpdate :one
SELECT * FROM auth.users WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = @uid FOR UPDATE;

-- name: GetUserByEmail :one
SELECT * FROM auth.users
WHERE project_number = @project_number AND tenant_id = @tenant_id AND email_lower = lower(@email::text);

-- name: GetUsersByUIDs :many
SELECT * FROM auth.users
WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = ANY(@uids::text[]);

-- name: GetUsersByEmails :many
SELECT * FROM auth.users
WHERE project_number = @project_number AND tenant_id = @tenant_id AND email_lower = ANY(@emails::text[]);

-- name: ListUsers :many
-- Stable order by uid, keyset-paginated.
SELECT * FROM auth.users
WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid > @after::text
ORDER BY uid
LIMIT @page_size;

-- name: CountUsers :one
SELECT count(*) FROM auth.users WHERE project_number = @project_number AND tenant_id = @tenant_id;

-- name: UpdateUser :one
UPDATE auth.users SET
    email = @email,
    email_verified = @email_verified,
    display_name = @display_name,
    photo_url = @photo_url,
    disabled = @disabled,
    custom_claims = @custom_claims
WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = @uid
RETURNING *;

-- name: TouchLogin :exec
UPDATE auth.users SET last_login_at = now(), last_refresh_at = now()
WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = @uid;

-- name: TouchRefresh :exec
UPDATE auth.users SET last_refresh_at = now()
WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = @uid;

-- name: RevokeUserTokens :one
-- Moves the revocation point to the transaction time. Refresh tokens created before it answer
-- TOKEN_EXPIRED; a session started later in the same transaction survives. ID tokens are
-- compared at second precision, as Firebase's validSince is.
UPDATE auth.users SET tokens_valid_after = now()
WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = @uid
RETURNING tokens_valid_after;

-- name: DeleteUser :execrows
DELETE FROM auth.users WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = @uid;

-- name: SetPassword :exec
INSERT INTO auth.user_passwords (project_number, tenant_id, uid, algorithm, hash)
VALUES (@project_number, @tenant_id, @uid, 'argon2id', @hash)
ON CONFLICT (project_number, tenant_id, uid) DO UPDATE SET algorithm = 'argon2id', hash = EXCLUDED.hash, updated_at = now();

-- name: GetPassword :one
SELECT * FROM auth.user_passwords WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = @uid;

-- name: DeletePassword :exec
DELETE FROM auth.user_passwords WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = @uid;

-- name: UpsertProvider :exec
INSERT INTO auth.user_providers (project_number, tenant_id, uid, provider_id, raw_id, email, display_name, photo_url)
VALUES (@project_number, @tenant_id, @uid, @provider_id, @raw_id, sqlc.narg(email), sqlc.narg(display_name), sqlc.narg(photo_url))
ON CONFLICT (project_number, tenant_id, uid, provider_id) DO UPDATE
SET raw_id = EXCLUDED.raw_id, email = EXCLUDED.email, display_name = EXCLUDED.display_name, photo_url = EXCLUDED.photo_url;

-- name: ListProviders :many
SELECT * FROM auth.user_providers
WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = @uid
ORDER BY provider_id;

-- name: ListProvidersForUsers :many
SELECT * FROM auth.user_providers
WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = ANY(@uids::text[])
ORDER BY uid, provider_id;

-- name: DeleteProviders :exec
DELETE FROM auth.user_providers
WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = @uid AND provider_id = ANY(@provider_ids::text[]);

-- name: GetUserByProvider :one
SELECT u.* FROM auth.users u
JOIN auth.user_providers p USING (project_number, tenant_id, uid)
WHERE u.project_number = @project_number AND u.tenant_id = @tenant_id AND p.provider_id = @provider_id AND p.raw_id = @raw_id;

-- name: PasswordUsersExist :many
-- Which of these users have a password, for the passwordHash placeholder in responses.
SELECT uid FROM auth.user_passwords
WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = ANY(@uids::text[]);
