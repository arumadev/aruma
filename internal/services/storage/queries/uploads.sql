-- The contract's queries (S2-0). S2-D adds the upload session statements here.

-- name: GetUploadSession :one
SELECT * FROM storage.upload_sessions WHERE id_hash = @id_hash AND project_number = @project_number;
