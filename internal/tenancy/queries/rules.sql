-- name: LockProjectRulesets :one
-- Serialises the ruleset count of one project: CreateRuleset counts and inserts under it, and
-- checks the project is still active.
SELECT state FROM control.projects WHERE project_number = @project_number FOR NO KEY UPDATE;

-- name: CountRulesets :one
SELECT count(*) FROM control.rulesets WHERE project_number = @project_number;

-- name: InsertRuleset :one
INSERT INTO control.rulesets (project_number, service, files)
VALUES (@project_number, @service, @files)
RETURNING *;

-- name: GetRuleset :one
SELECT * FROM control.rulesets WHERE project_number = @project_number AND ruleset_id = @ruleset_id;

-- name: GetRelease :one
SELECT * FROM control.rules_releases WHERE project_number = @project_number AND name = @name;

-- name: GetReleaseSource :one
-- The ruleset a release points to, which CompiledRelease compiles.
SELECT r.ruleset_id, s.service, s.files
FROM control.rules_releases r
JOIN control.rulesets s ON s.project_number = r.project_number AND s.ruleset_id = r.ruleset_id
WHERE r.project_number = @project_number AND r.name = @name;

-- name: UpsertRelease :one
INSERT INTO control.rules_releases (project_number, name, ruleset_id)
VALUES (@project_number, @name, @ruleset_id)
ON CONFLICT (project_number, name) DO UPDATE SET ruleset_id = excluded.ruleset_id, update_time = now()
RETURNING *;

-- name: PurgeProjectReleases :exec
DELETE FROM control.rules_releases WHERE project_number = @project_number;

-- name: PurgeProjectRulesets :exec
DELETE FROM control.rulesets WHERE project_number = @project_number;

-- name: ListRulesets :many
-- A project's rulesets newest first, without their files, keyset-paginated after the
-- (create_time, ruleset_id) of the last one a page showed.
SELECT ruleset_id, service, create_time FROM control.rulesets
WHERE project_number = @project_number
  AND (sqlc.narg(after_time)::timestamptz IS NULL
       OR create_time < sqlc.narg(after_time)::timestamptz
       OR (create_time = sqlc.narg(after_time)::timestamptz AND ruleset_id > sqlc.narg(after_id)::uuid))
ORDER BY create_time DESC, ruleset_id
LIMIT @page_size;

-- name: DeleteUnreleasedRuleset :execrows
-- Deletes a ruleset no release points to. A release published meanwhile makes the foreign key
-- refuse the delete instead (rules_releases_ruleset_fkey).
DELETE FROM control.rulesets s
WHERE s.project_number = @project_number AND s.ruleset_id = @ruleset_id
  AND NOT EXISTS (
    SELECT 1 FROM control.rules_releases r
    WHERE r.project_number = s.project_number AND r.ruleset_id = s.ruleset_id);

-- name: ListReleases :many
-- A project's releases by their stored name, keyset-paginated by it.
SELECT * FROM control.rules_releases
WHERE project_number = @project_number AND name > @after::text
ORDER BY name
LIMIT @page_size;
