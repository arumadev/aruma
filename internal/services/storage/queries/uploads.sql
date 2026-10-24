-- The upload sessions (ADR-022 §6, §7). A session's id is a capability: only its HMAC is kept,
-- and every statement on one session names its project too, so an id of one project finds
-- nothing in another.

-- name: GetUploadSession :one
SELECT * FROM storage.upload_sessions WHERE id_hash = @id_hash AND project_number = @project_number;

-- name: InsertUploadSession :exec
INSERT INTO storage.upload_sessions (id_hash, project_number, name, surface, principal_kind, principal,
    client_ip, declared_size, attrs, download_tokens, preconditions, checksums, staging_key,
    created_at, progressed_at, expires_at)
VALUES (@id_hash, @project_number, @name, @surface, @principal_kind, @principal,
    sqlc.narg('client_ip'), sqlc.narg('declared_size'), @attrs, @download_tokens, @preconditions, @checksums, @staging_key,
    @now, @now, @expires_at);

-- name: CountLiveUploads :one
-- The live sessions that received a byte, in the project, of one uid and from one address (the
-- per-IP cap counts per project). Empty sessions do not count, so sessions that never send a
-- byte cannot fill the limits (ADR-022 §6.5).
SELECT count(*) AS project,
    count(*) FILTER (WHERE principal ->> 'uid' = sqlc.narg('uid')::text) AS by_user,
    count(*) FILTER (WHERE client_ip = sqlc.narg('client_ip')::inet) AS by_ip
FROM storage.upload_sessions
WHERE project_number = @project_number AND state = 'active' AND received > 0;

-- name: ReserveUploadSession :execrows
-- Quota a chunk takes for the bytes on their way in, held by the session (Boot reconciles the
-- project's reserved bytes to the sum of these).
UPDATE storage.upload_sessions SET reserved_bytes = reserved_bytes + @n::bigint
WHERE id_hash = @id_hash AND project_number = @project_number AND state = 'active';

-- name: RecordUploadProgress :execrows
-- The durable bytes after a chunk, announced only once the staging file is synced, with the
-- hashes' state at that length and what the session still holds of the quota.
UPDATE storage.upload_sessions SET received = @received, hash_state = @hash_state,
    reserved_bytes = @reserved_bytes, progressed_at = @now, expires_at = @expires_at
WHERE id_hash = @id_hash AND project_number = @project_number AND state = 'active';

-- name: SetUploadBlobKey :execrows
-- The object key the staging file moves to, written before it moves, so a finalize after a crash
-- finds the file (ADR-022 §5).
UPDATE storage.upload_sessions SET blob_key = @blob_key
WHERE id_hash = @id_hash AND project_number = @project_number AND state = 'active';

-- name: FinishUploadSession :execrows
-- A finalize that committed, in commitObject's own transaction: the object as the reply shows it,
-- for every repeat.
UPDATE storage.upload_sessions SET state = 'final', result_object = @result_object,
    download_tokens = @download_tokens, reserved_bytes = 0, expires_at = @expires_at
WHERE id_hash = @id_hash AND project_number = @project_number AND state = 'active';

-- name: FailUploadSession :execrows
-- A terminal refusal (ADR-022 §6.1): the core error every repeat answers with.
UPDATE storage.upload_sessions SET state = 'final', final_status = @final_status, final_body = @final_body,
    reserved_bytes = 0, expires_at = @expires_at
WHERE id_hash = @id_hash AND project_number = @project_number AND state = 'active';

-- name: CancelUploadSession :execrows
UPDATE storage.upload_sessions SET state = 'cancelled', reserved_bytes = 0
WHERE id_hash = @id_hash AND project_number = @project_number AND state = 'active';

-- name: ReleaseIdleUpload :execrows
-- An idle session gives back what it holds beyond its durable bytes (upload_idle_ttl).
UPDATE storage.upload_sessions SET reserved_bytes = received
WHERE id_hash = @id_hash AND project_number = @project_number AND state = 'active';

-- name: DeleteUploadSession :exec
DELETE FROM storage.upload_sessions WHERE id_hash = @id_hash AND project_number = @project_number;

-- name: ListExpiredUploads :many
-- scope: global — the expiry works across projects on sessions whose time is up, oldest first.
SELECT id_hash, project_number FROM storage.upload_sessions
WHERE expires_at <= @now ORDER BY expires_at LIMIT @n;

-- name: ListIdleUploads :many
-- scope: global — the idle release works across projects on sessions without a durable byte since
-- the cutoff that hold more than they received, or received nothing.
SELECT id_hash, project_number FROM storage.upload_sessions
WHERE state = 'active' AND progressed_at <= @before AND (reserved_bytes > received OR received = 0)
ORDER BY progressed_at LIMIT @n;

-- name: UploadsSummary :one
-- scope: global — the live sessions and their staged bytes in the process, for the gauges.
SELECT count(*) AS sessions, COALESCE(sum(received), 0)::bigint AS staged
FROM storage.upload_sessions WHERE state = 'active';
