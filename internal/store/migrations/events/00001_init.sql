-- +goose Up

CREATE SCHEMA IF NOT EXISTS events;

-- Shared change log. Rows are written inside the same transaction as the change they describe,
-- so a rolled-back write leaves no event behind.
CREATE TABLE events.changes (
    seq            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_number bigint NOT NULL,
    service        text NOT NULL,
    type           text NOT NULL,
    subject        text NOT NULL,
    data           jsonb NOT NULL DEFAULT '{}',
    committed_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX changes_project_seq_idx ON events.changes (project_number, seq);

-- +goose Down

DROP SCHEMA events CASCADE;
