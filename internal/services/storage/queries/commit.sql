-- The statements of commitObject and releaseObject (ADR-022 §5), written with the contract so
-- S2-C and S2-D only use them. No RETURNING old.*, which sqlc cannot generate: a write inserts
-- with DO NOTHING; on a conflict, the same transaction locks the row, checks the
-- preconditions on it, and updates it. Under READ COMMITTED, FOR UPDATE waits for a concurrent
-- writer and then reads its committed version, so the key it replaced is the one that goes to
-- blob_gc.

-- name: InsertObject :one
-- A new name. No row back is a conflict: lock the row, or, under ifGenerationMatch=0, 412.
INSERT INTO storage.objects (project_number, name, generation, metageneration, blob_key, size, md5, crc32c,
    content_type, content_encoding, content_disposition, content_language, cache_control,
    metadata, download_tokens, time_created, updated)
VALUES (@project_number, @name, @generation, 1, @blob_key, @size, @md5, @crc32c,
    @content_type, @content_encoding, @content_disposition, @content_language, @cache_control,
    @metadata, @download_tokens, @now, @now)
ON CONFLICT (project_number, name) DO NOTHING
RETURNING *;

-- name: LockObject :one
-- The current row, locked until the transaction ends: its blob_key and size are what the
-- write replaces, and the preconditions are checked on it.
SELECT * FROM storage.objects WHERE project_number = @project_number AND name = @name FOR UPDATE;

-- name: ReplaceObject :one
-- A new generation over the locked row: always above the previous one (ADR-022 §12).
UPDATE storage.objects SET
    generation = GREATEST(@generation::bigint, generation + 1), metageneration = 1,
    blob_key = @blob_key, size = @size, md5 = @md5, crc32c = @crc32c,
    content_type = @content_type, content_encoding = @content_encoding,
    content_disposition = @content_disposition, content_language = @content_language,
    cache_control = @cache_control, metadata = @metadata, download_tokens = @download_tokens,
    time_created = @now, updated = @now
WHERE project_number = @project_number AND name = @name
RETURNING *;

-- name: QueueBlobGC :exec
-- A file no row points to any more, deleted after gc_grace.
INSERT INTO storage.blob_gc (blob_key, project_number) VALUES (@blob_key, @project_number);

-- name: ReleaseUsage :execrows
-- An object's bytes leave the quota (a delete, or the generation an overwrite replaced).
UPDATE storage.usage SET used_bytes = used_bytes - @size::bigint, object_count = object_count - 1
WHERE project_number = @project_number;
