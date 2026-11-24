-- The data directory's binding, the deletion outbox, the sweep and the purge (ADR-022 §3, §5).
-- The worker and the sweep run across projects on file keys, not on users' data, so their
-- statements carry a global scope.

-- name: GetInstance :one
-- scope: global — the one row binding this database to its data_dir belongs to no project.
SELECT * FROM storage.instance;

-- name: InsertInstance :exec
-- scope: global — the one row binding this database to its data_dir belongs to no project.
INSERT INTO storage.instance (id) VALUES (@id);

-- name: PointsToFiles :one
-- scope: global — Boot asks whether this database expects any file in data_dir, of any project:
-- an object's, or the staging file of a live upload session.
SELECT EXISTS (
    SELECT 1 FROM storage.objects
    UNION ALL
    SELECT 1 FROM storage.upload_sessions WHERE state = 'active'
) AS points;

-- name: QueueUnreferencedBlob :exec
-- A file a session leaves behind (its staging file, or the object key it never committed), queued
-- unless an object points to it or it is queued already.
INSERT INTO storage.blob_gc (blob_key, project_number)
SELECT @blob_key::text, @project_number::bigint
WHERE NOT EXISTS (SELECT 1 FROM storage.objects o WHERE o.blob_key = @blob_key::text AND o.project_number = @project_number::bigint)
ON CONFLICT (blob_key) DO NOTHING;

-- name: ListDueBlobGC :many
-- scope: global — the GC worker deletes files across projects once gc_grace has passed. A key a
-- row points to again is never due, so a bug that queued a live key deletes nothing.
SELECT g.blob_key, g.project_number FROM storage.blob_gc g
WHERE g.queued_at <= @before
    AND NOT EXISTS (SELECT 1 FROM storage.objects o WHERE o.blob_key = g.blob_key)
    AND NOT EXISTS (SELECT 1 FROM storage.upload_sessions u WHERE u.staging_key = g.blob_key AND u.state = 'active')
    AND NOT EXISTS (SELECT 1 FROM storage.upload_sessions u WHERE u.blob_key = g.blob_key AND u.state = 'active')
ORDER BY g.queued_at LIMIT @n;

-- name: DeleteBlobGC :exec
-- scope: global — the outbox rows of files the GC worker deleted, by their unique keys.
DELETE FROM storage.blob_gc WHERE blob_key = ANY(@blob_keys::text[]);

-- name: BlobGCStats :one
-- scope: global — the outbox's length and the age of its oldest row, for the gauges.
SELECT count(*) AS queued, COALESCE(min(queued_at), @now::timestamptz)::timestamptz AS oldest FROM storage.blob_gc;

-- name: UnreferencedKeys :many
-- scope: global — the sweep checks file keys across projects: those no object, no session (its
-- staging file or its object key) and no outbox row points to. A queued file is the GC's.
SELECT k::text AS blob_key FROM unnest(@blob_keys::text[]) AS k
WHERE NOT EXISTS (SELECT 1 FROM storage.objects o WHERE o.blob_key = k)
    AND NOT EXISTS (SELECT 1 FROM storage.upload_sessions u WHERE u.staging_key = k OR u.blob_key = k)
    AND NOT EXISTS (SELECT 1 FROM storage.blob_gc g WHERE g.blob_key = k);

-- name: ListObjectKeys :many
-- scope: global — the periodic sweep pages through every object's file key, to report rows whose
-- file is missing.
SELECT blob_key FROM storage.objects WHERE blob_key > @after::text ORDER BY blob_key LIMIT @n;

-- name: PurgeUploadSessions :exec
DELETE FROM storage.upload_sessions WHERE project_number = @project_number;

-- name: PurgeObjects :exec
DELETE FROM storage.objects WHERE project_number = @project_number;

-- name: PurgeBlobGC :exec
DELETE FROM storage.blob_gc WHERE project_number = @project_number;

-- name: PurgeUsage :exec
DELETE FROM storage.usage WHERE project_number = @project_number;

-- name: PurgeBuckets :exec
DELETE FROM storage.buckets WHERE project_number = @project_number;

-- name: TryLockFilesAgainstBackup :one
-- scope: global — a deletion of files (the blob_gc worker, a purge) runs only while no backup
-- holds storage.backup (HoldFilesForBackup), and holds the lock to its commit, so a backup never
-- waits on a deletion halfway and never misses a file its dump names.
SELECT pg_try_advisory_xact_lock(hashtext('storage.backup'), 0) AS free;

-- name: HoldFilesForBackup :exec
-- scope: global — a backup holds storage.backup, shared, on its own session from before its
-- dump until its archive is written: no file is deleted meanwhile.
SELECT pg_advisory_lock_shared(hashtext('storage.backup'), 0);

-- name: ExportSnapshot :one
-- scope: global — the snapshot pg_dump takes (--snapshot), so the files a backup copies are the
-- ones its dump names.
SELECT pg_export_snapshot()::text AS snapshot;

-- name: ListBackupKeys :many
-- scope: global — every file the database names, of every project: the objects' files, and the
-- staging and object files of live upload sessions. A backup copies these and nothing else.
SELECT blob_key AS key FROM storage.objects
UNION
SELECT staging_key FROM storage.upload_sessions WHERE state = 'active'
UNION
SELECT blob_key FROM storage.upload_sessions WHERE state = 'active' AND blob_key IS NOT NULL
ORDER BY 1;

-- name: GetPromotedKey :one
-- scope: global — where a staging file went: the object key its session moved it to.
SELECT blob_key FROM storage.upload_sessions WHERE staging_key = @staging_key::text AND blob_key IS NOT NULL;
