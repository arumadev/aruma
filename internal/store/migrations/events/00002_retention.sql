-- +goose Up

-- Retention (housekeeping, retention.events_days): the purge reads the oldest changes of every
-- project at once.
CREATE INDEX changes_committed_at_idx ON events.changes (committed_at);

-- +goose Down

DROP INDEX events.changes_committed_at_idx;
