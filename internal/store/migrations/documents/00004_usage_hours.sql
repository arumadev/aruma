-- +goose Up

-- The dashboard's usage, an hour a row (ADR-030 §8): what Firestore bills (documents read,
-- written and deleted), the peaks of snapshot listeners and of Listen streams, and the security
-- rules' decisions. The process counts in memory and adds its counts here every few seconds, in
-- one statement outside any commit, so that no request writes a row. Rows older than 31 days are
-- pruned. documents.usage (00001), a row a day that nothing writes, stays unused.
CREATE TABLE documents.usage_hours (
    project_number   bigint NOT NULL,
    hour             timestamptz NOT NULL CHECK (date_trunc('hour', hour) = hour),
    reads            bigint NOT NULL DEFAULT 0 CHECK (reads >= 0),
    writes           bigint NOT NULL DEFAULT 0 CHECK (writes >= 0),
    deletes          bigint NOT NULL DEFAULT 0 CHECK (deletes >= 0),
    listeners_peak   bigint NOT NULL DEFAULT 0 CHECK (listeners_peak >= 0),
    connections_peak bigint NOT NULL DEFAULT 0 CHECK (connections_peak >= 0),
    rules_allows     bigint NOT NULL DEFAULT 0 CHECK (rules_allows >= 0),
    rules_denies     bigint NOT NULL DEFAULT 0 CHECK (rules_denies >= 0),
    rules_errors     bigint NOT NULL DEFAULT 0 CHECK (rules_errors >= 0),
    PRIMARY KEY (project_number, hour)
);

-- The pruning reads the oldest hours of every project.
CREATE INDEX usage_hours_hour ON documents.usage_hours (hour);

-- +goose Down

DROP TABLE documents.usage_hours;
