-- name: CreateProject :one
INSERT INTO control.projects (project_number, project_id, display_name, owner_account_id, etag, auth_domain)
VALUES (@project_number, @project_id, @display_name, @owner_account_id, @etag, @auth_domain)
RETURNING *;

-- name: GetOwnedProject :one
-- scope: owner — the ownership gate: resolves a project id or number only for its owner.
SELECT * FROM control.projects
WHERE owner_account_id = @owner_account_id
  AND (project_id = @ref::text OR project_number::text = @ref::text);

-- name: GetProject :one
SELECT * FROM control.projects WHERE project_number = @project_number;

-- name: GetProjectByID :one
-- scope: global — the public token discovery endpoints name a project by id.
SELECT * FROM control.projects WHERE project_id = @project_id;

-- name: ProjectIDTaken :one
-- scope: global — availability check; deleted ids stay taken forever.
SELECT EXISTS (SELECT 1 FROM control.projects WHERE project_id = @project_id);

-- name: ListOwnedProjects :many
-- scope: owner — the caller's projects, keyset-paginated by project id.
SELECT * FROM control.projects
WHERE owner_account_id = @owner_account_id
  AND project_id > @after::text
  AND (state = 'ACTIVE' OR @show_deleted::boolean)
ORDER BY project_id
LIMIT @page_size;

-- name: UpdateProject :one
UPDATE control.projects
SET display_name = coalesce(sqlc.narg(display_name), display_name),
    auth_domain = coalesce(sqlc.narg(auth_domain), auth_domain),
    etag = @new_etag,
    updated_at = now()
WHERE project_number = @project_number AND etag = @etag
RETURNING *;

-- name: DeleteProject :one
UPDATE control.projects
SET state = 'DELETED', delete_time = now(), expire_time = now() + make_interval(days => @retention_days::int),
    etag = @new_etag, updated_at = now()
WHERE project_number = @project_number AND state = 'ACTIVE'
RETURNING *;

-- name: RestoreProject :one
UPDATE control.projects
SET state = 'ACTIVE', delete_time = NULL, expire_time = NULL, etag = @new_etag, updated_at = now()
WHERE project_number = @project_number AND state = 'DELETED' AND expire_time > now()
RETURNING *;

-- name: ListExpiredProjects :many
-- scope: global — the purge job looks across all projects for expired deletions.
SELECT project_number FROM control.projects
WHERE state = 'DELETED' AND expire_time <= now()
  AND EXISTS (SELECT 1 FROM control.apps a WHERE a.project_number = control.projects.project_number
              UNION ALL SELECT 1 FROM control.api_keys k WHERE k.project_number = control.projects.project_number
              UNION ALL SELECT 1 FROM control.service_accounts s WHERE s.project_number = control.projects.project_number)
LIMIT 100;

-- name: PurgeProjectApps :exec
DELETE FROM control.apps WHERE project_number = @project_number;

-- name: PurgeProjectAPIKeys :exec
DELETE FROM control.api_keys WHERE project_number = @project_number;

-- name: PurgeProjectServiceAccounts :exec
DELETE FROM control.service_accounts WHERE project_number = @project_number;

-- name: InsertAudit :exec
INSERT INTO control.audit_log (actor_type, actor_id, project_number, action, target, result, ip, details)
VALUES (@actor_type, @actor_id, @project_number, @action, @target, @result, @ip, @details);

-- name: ListAudit :many
-- The audit log of one project, newest first, keyset-paginated by id.
SELECT * FROM control.audit_log
WHERE project_number = @project_number AND id < @before_id
ORDER BY id DESC
LIMIT @page_size;
