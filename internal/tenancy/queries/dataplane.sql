-- name: ListActiveProjectsByAuthDomain :many
-- scope: global — an authDomain names the project whose Auth pages it serves.
SELECT * FROM control.projects WHERE auth_domain = @auth_domain AND state = 'ACTIVE' ORDER BY project_number LIMIT 2;
