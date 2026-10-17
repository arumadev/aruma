-- name: CreateApp :one
INSERT INTO control.apps (app_id, project_number, platform, display_name, package_name, bundle_id, api_key_id, etag)
VALUES (@app_id, @project_number, @platform, @display_name, @package_name, @bundle_id, @api_key_id, @etag)
RETURNING *;

-- name: GetApp :one
SELECT * FROM control.apps WHERE project_number = @project_number AND app_id = @app_id;

-- name: ListApps :many
-- Apps of a project, optionally of one platform, keyset-paginated by app id.
SELECT * FROM control.apps
WHERE project_number = @project_number
  AND (sqlc.narg(platform)::text IS NULL OR platform = sqlc.narg(platform)::text)
  AND app_id > @after::text
  AND (state = 'ACTIVE' OR @show_deleted::boolean)
ORDER BY app_id
LIMIT @page_size;

-- name: ListActiveAppsByPlatform :many
SELECT * FROM control.apps
WHERE project_number = @project_number AND platform = @platform AND state = 'ACTIVE'
ORDER BY created_at, app_id;

-- name: UpdateApp :one
UPDATE control.apps
SET display_name = coalesce(sqlc.narg(display_name), display_name), etag = @new_etag, updated_at = now()
WHERE project_number = @project_number AND app_id = @app_id AND etag = @etag
RETURNING *;

-- name: DeleteApp :one
UPDATE control.apps
SET state = 'DELETED', delete_time = now(), expire_time = now() + make_interval(days => @retention_days::int),
    etag = @new_etag, updated_at = now()
WHERE project_number = @project_number AND app_id = @app_id AND state = 'ACTIVE'
RETURNING *;

-- name: RestoreApp :one
UPDATE control.apps
SET state = 'ACTIVE', delete_time = NULL, expire_time = NULL, etag = @new_etag, updated_at = now()
WHERE project_number = @project_number AND app_id = @app_id AND state = 'DELETED' AND expire_time > now()
RETURNING *;

-- name: PurgeApp :exec
DELETE FROM control.apps WHERE project_number = @project_number AND app_id = @app_id;

-- name: PurgeExpiredApps :execrows
-- scope: global — the purge job removes apps whose retention ended, in every project.
DELETE FROM control.apps WHERE state = 'DELETED' AND expire_time <= now();

-- name: SetAppsAPIKey :exec
UPDATE control.apps SET api_key_id = @new_key, etag = @new_etag, updated_at = now()
WHERE project_number = @project_number AND api_key_id = @old_key;

-- name: AddShaCertificate :one
INSERT INTO control.app_sha_certificates (app_id, sha_hash, cert_type)
SELECT a.app_id, @sha_hash, @cert_type FROM control.apps a
WHERE a.project_number = @project_number AND a.app_id = @app_id
RETURNING *;

-- name: ListShaCertificates :many
SELECT c.* FROM control.app_sha_certificates c
JOIN control.apps a ON a.app_id = c.app_id
WHERE a.project_number = @project_number AND c.app_id = @app_id
ORDER BY c.created_at, c.id;

-- name: ListProjectShaCertificates :many
SELECT c.* FROM control.app_sha_certificates c
JOIN control.apps a ON a.app_id = c.app_id
WHERE a.project_number = @project_number
ORDER BY c.app_id, c.created_at, c.id;

-- name: DeleteShaCertificate :execrows
DELETE FROM control.app_sha_certificates c
USING control.apps a
WHERE a.app_id = c.app_id AND a.project_number = @project_number AND c.app_id = @app_id AND c.id = @id;
