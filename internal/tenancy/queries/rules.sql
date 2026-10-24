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
