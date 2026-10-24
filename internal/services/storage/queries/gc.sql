-- The contract's queries (S2-0). S2-D adds the outbox, sweep and expiry statements here; those
-- that run across projects carry a scope annotation saying why.

-- name: GetInstance :one
-- scope: global — the one row binding this database to its data_dir belongs to no project.
SELECT * FROM storage.instance;
