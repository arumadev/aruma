-- name: CreateAPIKey :one
INSERT INTO control.api_keys (project_number, key_string, display_name, restrictions, platform, rotated_from, etag)
VALUES (@project_number, @key_string, @display_name, @restrictions, sqlc.narg(platform), sqlc.narg(rotated_from), @etag)
RETURNING *;

-- name: GetAPIKey :one
SELECT * FROM control.api_keys WHERE project_number = @project_number AND uid = @uid;

-- name: GetPlatformAPIKey :one
SELECT * FROM control.api_keys WHERE project_number = @project_number AND platform = @platform;

-- name: ListAPIKeys :many
SELECT * FROM control.api_keys
WHERE project_number = @project_number AND state <> 'DELETED'
ORDER BY created_at, uid;

-- name: UpdateAPIKey :one
UPDATE control.api_keys
SET display_name = coalesce(sqlc.narg(display_name), display_name),
    restrictions = coalesce(sqlc.narg(restrictions), restrictions),
    etag = @new_etag, updated_at = now()
WHERE project_number = @project_number AND uid = @uid AND etag = @etag AND state <> 'DELETED'
RETURNING *;

-- name: SetAPIKeyState :one
UPDATE control.api_keys SET state = @state, etag = @new_etag, updated_at = now()
WHERE project_number = @project_number AND uid = @uid AND state <> 'DELETED'
RETURNING *;

-- name: ReleaseAPIKeyPlatform :exec
UPDATE control.api_keys SET platform = NULL, etag = @new_etag, updated_at = now()
WHERE project_number = @project_number AND uid = @uid;

-- name: LookupAPIKey :one
-- scope: global — resolves the project of a key presented by a client SDK; the key is the only
-- input, and a key identifies exactly one project.
SELECT k.uid, k.project_number, k.state, k.restrictions, p.state AS project_state
FROM control.api_keys k
JOIN control.projects p ON p.project_number = k.project_number
WHERE k.key_string = @key_string;
