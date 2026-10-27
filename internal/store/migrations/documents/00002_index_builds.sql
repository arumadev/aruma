-- +goose Up

-- Index builds (ADR-024 §2.5; ADR-025 §2, §3, §4.5): what a background build needs to resume
-- after a restart, and what the Admin API's operations report.
--
-- Single-field indexes have no indexes row: an entry's index_id is a negative number derived
-- from (collection group, scope, field path, mode) (core.SingleFieldIndexID), so a commit writes
-- the automatic indexes without touching a cold table. Their configuration is field_overrides',
-- and a change of it is built as a composite is: the new configuration from one catalog
-- version on, the documents rebuilt in name-key batches, then the next version.

-- A composite's build: the stored name key of the last document its backfill wrote, NULL before
-- the first batch; and the CreateIndex operation that completes when it is READY.
ALTER TABLE documents.indexes
    ADD COLUMN build_cursor bytea,
    ADD COLUMN operation_id text COLLATE "C";

-- A field configuration being built: building is set from the catalog version that applies the
-- new index_config until the one after its last batch; prior_index_config is the configuration
-- before (NULL: inherited), whose entries commits keep deleting meanwhile; reverting marks a
-- removal, whose index_config holds the inherited indexes until the build ends and is then NULL.
-- One build at a time per database.
ALTER TABLE documents.field_overrides
    ADD COLUMN building           boolean NOT NULL DEFAULT false,
    ADD COLUMN prior_index_config bytea,
    ADD COLUMN reverting          boolean NOT NULL DEFAULT false,
    ADD COLUMN build_cursor       bytea,
    ADD COLUMN operation_id       text COLLATE "C",
    ADD CONSTRAINT field_overrides_build CHECK (building OR (prior_index_config IS NULL AND NOT reverting AND build_cursor IS NULL));

CREATE UNIQUE INDEX field_overrides_building ON documents.field_overrides (project_number, database_id) WHERE building;

-- A queued field update's google.firestore.admin.v1.UpdateFieldRequest, binary protobuf, until
-- the build starts it; NULL for any other operation and once started.
ALTER TABLE documents.operations
    ADD COLUMN request bytea;

CREATE INDEX operations_queued ON documents.operations (project_number, database_id, create_time, id)
    WHERE request IS NOT NULL;

-- +goose Down

DROP INDEX documents.operations_queued;
ALTER TABLE documents.operations DROP COLUMN request;
DROP INDEX documents.field_overrides_building;
ALTER TABLE documents.field_overrides
    DROP CONSTRAINT field_overrides_build,
    DROP COLUMN operation_id,
    DROP COLUMN build_cursor,
    DROP COLUMN reverting,
    DROP COLUMN prior_index_config,
    DROP COLUMN building;
ALTER TABLE documents.indexes
    DROP COLUMN operation_id,
    DROP COLUMN build_cursor;
