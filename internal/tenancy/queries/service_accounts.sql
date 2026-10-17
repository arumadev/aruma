-- name: CreateServiceAccount :one
INSERT INTO control.service_accounts (project_number, email, unique_id, display_name)
VALUES (@project_number, @email, @unique_id, @display_name)
RETURNING *;

-- name: GetServiceAccount :one
SELECT * FROM control.service_accounts WHERE project_number = @project_number AND id = @id;

-- name: ListServiceAccounts :many
SELECT * FROM control.service_accounts WHERE project_number = @project_number ORDER BY created_at, id;

-- name: DeleteServiceAccount :execrows
DELETE FROM control.service_accounts WHERE project_number = @project_number AND id = @id;

-- name: CountServiceAccountKeys :one
SELECT count(*) FROM control.service_account_keys k
JOIN control.service_accounts s ON s.id = k.service_account_id
WHERE s.project_number = @project_number AND k.service_account_id = @service_account_id;

-- name: CreateServiceAccountKey :one
INSERT INTO control.service_account_keys (key_id, service_account_id, certificate_pem, valid_after, valid_before)
SELECT @key_id, s.id, @certificate_pem, @valid_after, @valid_before
FROM control.service_accounts s
WHERE s.project_number = @project_number AND s.id = @service_account_id
RETURNING *;

-- name: ListServiceAccountKeys :many
SELECT k.* FROM control.service_account_keys k
JOIN control.service_accounts s ON s.id = k.service_account_id
WHERE s.project_number = @project_number AND k.service_account_id = @service_account_id
ORDER BY k.created_at, k.key_id;

-- name: DeleteServiceAccountKey :execrows
DELETE FROM control.service_account_keys k
USING control.service_accounts s
WHERE s.id = k.service_account_id AND s.project_number = @project_number
  AND k.service_account_id = @service_account_id AND k.key_id = @key_id;

-- name: GetServiceAccountByEmail :one
-- scope: global — the token endpoint identifies the caller by the assertion's issuer (its email),
-- which is unique across projects; the project comes from the row.
SELECT s.*, p.state AS project_state
FROM control.service_accounts s
JOIN control.projects p ON p.project_number = s.project_number
WHERE s.email = @email;

-- name: ListActiveKeysByEmail :many
-- scope: global — the public certificates of a service account, served at its x509 URL and used
-- to verify its assertions; certificates are public.
SELECT k.* FROM control.service_account_keys k
JOIN control.service_accounts s ON s.id = k.service_account_id
WHERE s.email = @email AND NOT k.disabled
  AND k.valid_after <= now() AND (k.valid_before IS NULL OR k.valid_before > now())
ORDER BY k.created_at;

-- name: CreateOAuthToken :exec
INSERT INTO control.oauth_access_tokens (token_hash, principal_id, project_number, scopes, expires_at)
VALUES (@token_hash, @principal_id, @project_number, @scopes, @expires_at);

-- name: GetActiveOAuthToken :one
-- scope: global — resolves a bearer token presented to the API; the hash is the only input.
SELECT t.*, s.email, s.disabled AS account_disabled, p.state AS project_state
FROM control.oauth_access_tokens t
JOIN control.service_accounts s ON s.id = t.principal_id
JOIN control.projects p ON p.project_number = t.project_number
WHERE t.token_hash = @token_hash AND t.expires_at > now();

-- name: DeleteExpiredOAuthTokens :execrows
-- scope: global — housekeeping across projects.
DELETE FROM control.oauth_access_tokens WHERE expires_at <= now() - interval '1 day';
