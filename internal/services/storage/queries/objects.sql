-- The object, list, token and CORS statements (S2-0's reads, then S2-C's). Every change to a
-- row is conditional on the generation and metageneration its caller read, decided on and
-- authorized: no row back means the row changed in between, and the caller reads it again.

-- name: GetObject :one
SELECT * FROM storage.objects WHERE project_number = @project_number AND name = @name;

-- name: GetBucketSettings :one
SELECT * FROM storage.buckets WHERE project_number = @project_number;

-- name: UpdateObject :one
-- A metadata change (PATCH), a token change (create_token, delete_token, a GCS patch of
-- firebaseStorageDownloadTokens), or the token an authorized /v0 read mints: one statement, a
-- new metageneration, and the same generation (ADR-022 §9, §12). Two readers that race to mint
-- read the same metageneration, so only one of them writes: exactly one token.
UPDATE storage.objects SET
    metageneration = metageneration + 1,
    content_type = @content_type, content_encoding = @content_encoding,
    content_disposition = @content_disposition, content_language = @content_language,
    cache_control = @cache_control, metadata = @metadata, download_tokens = @download_tokens,
    updated = GREATEST(@now::timestamptz, updated)
WHERE project_number = @project_number AND name = @name
    AND generation = @generation AND metageneration = @metageneration
RETURNING *;

-- name: DeleteObject :one
-- The generation the delete was authorized on; its file goes to blob_gc in the same
-- transaction (releaseObject).
DELETE FROM storage.objects
WHERE project_number = @project_number AND name = @name
    AND generation = @generation AND metageneration = @metageneration
RETURNING blob_key, size;

-- name: ListObjects :many
-- One seek of the list's index walk (ADR-022 §13): the names in [from_name, before_name), in
-- byte order (COLLATE "C"). A prefix found in them makes the next seek start past it.
SELECT * FROM storage.objects
WHERE project_number = @project_number AND name >= @from_name AND name < @before_name
ORDER BY name
LIMIT @max_rows;

-- name: SetBucketCORS :one
-- The bucket's settings row, created on its first change: metageneration 1 is the bucket
-- before any change, so the first one makes it 2 (BucketInfo).
INSERT INTO storage.buckets (project_number, cors, metageneration, updated)
VALUES (@project_number, @cors, 2, @now)
ON CONFLICT (project_number) DO UPDATE SET
    cors = EXCLUDED.cors, metageneration = storage.buckets.metageneration + 1,
    updated = GREATEST(EXCLUDED.updated, storage.buckets.updated)
RETURNING *;
