-- +goose Up

CREATE SCHEMA IF NOT EXISTS rtdb;

-- Realtime Database storage (ADR-019): one row per leaf, keyed by its path. Objects are not
-- stored; they follow from their leaves. Children are ordered in Go by Firebase's rules,
-- never by these rows' text order.

-- One row per database. In v0.2 a project has one, <project_id>-default-rtdb (ADR-018).
CREATE TABLE rtdb.databases (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_number bigint NOT NULL,
    name           text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    -- The rules document as written (it may hold comments). A new database is locked
    -- (ADR-020); admin and service credentials bypass rules.
    rules          text NOT NULL DEFAULT '{"rules": {".read": false, ".write": false}}',
    -- The number of writes applied, advanced by the database's single writer in the same
    -- transaction as each write (ADR-019).
    seq            bigint NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT databases_name_key UNIQUE (name),
    CONSTRAINT databases_project_name_key UNIQUE (project_number, name)
);

-- path is the keys from the root joined by '/', '' for the root. With COLLATE "C" a subtree is
-- one index range: path = p, or p || '/' <= path < p || '0' ('0' follows '/' in ASCII, and no
-- key holds a control character).
CREATE TABLE rtdb.nodes (
    database_id bigint NOT NULL REFERENCES rtdb.databases (id) ON DELETE CASCADE,
    path        text COLLATE "C" NOT NULL,
    -- The leaf's value; NULL marks the row of an object that carries a priority. jsonb text
    -- cannot hold U+0000, so a string with one is a one-element array of its base64 bytes.
    value       jsonb CHECK (value IS NULL OR jsonb_typeof(value) IN ('boolean', 'number', 'string')
                             OR jsonb_typeof(value) = 'array' AND jsonb_array_length(value) = 1),
    priority    jsonb CHECK (priority IS NULL OR jsonb_typeof(priority) IN ('number', 'string')
                             OR jsonb_typeof(priority) = 'array' AND jsonb_array_length(priority) = 1),
    PRIMARY KEY (database_id, path),
    CONSTRAINT nodes_row_kind CHECK (value IS NOT NULL OR priority IS NOT NULL)
);

-- +goose Down

DROP SCHEMA rtdb CASCADE;
