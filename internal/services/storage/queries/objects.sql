-- The contract's queries (S2-0). S2-C adds the object, list, token and CORS statements here.

-- name: GetObject :one
SELECT * FROM storage.objects WHERE project_number = @project_number AND name = @name;

-- name: GetBucketSettings :one
SELECT * FROM storage.buckets WHERE project_number = @project_number;
