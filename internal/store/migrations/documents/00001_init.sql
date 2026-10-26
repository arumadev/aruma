-- +goose Up

CREATE SCHEMA IF NOT EXISTS documents;

-- The Document Database, Firestore's wire protocol on Postgres (ADR-024 §1). Every key starts with
-- (project_number, database_id); database_id is '(default)' alone in v0.4.0, so named databases
-- (v0.4.x) rewrite no key. Times are timestamptz: microseconds, as production keeps update_time.
--
-- The hot tables (documents, versions, changelog, index_entries) carry no foreign key: a check
-- would run inside the commit's stamp statement, which holds the database's one sequencer row
-- (ADR-024 §2.4). Deleting a project or a database is core's explicit cleanup in batches. The
-- cold tables (sequencer, indexes, field_overrides, operations) reference databases.
--
-- Sort keys (ADR-024 §1.3, value.Key): byte strings whose bytes.Compare order is Firestore's. A
-- document's name key is the key version byte 0x01, then per path segment 0x01 and the 8
-- sign-flipped bytes of a numeric id __id<n>__, or 0x02, the escaped text and 0x00 0x01; then
-- 0x00. So a document sorts before its descendants, and the rows below a document or a
-- collection are one key range.
--
-- The btree bound (ADR-024 §1.4): a Postgres index entry holds at most 2,704 bytes, and
-- production accepts names and index entries longer than that. A key column that holds a sort
-- key stores the key whole when it has at most 2,048 bytes, else its first 2,048 bytes and the
-- SHA-256 of the whole key (2,080 bytes); the whole key is in an unindexed column. Keys that
-- share their first 2,048 bytes are ordered by hash there, and core orders them by the whole key.

-- One row per database (ADR-024 §1.2; ADR-028 §2). core creates '(default)' and its sequencer
-- row together, in one statement, when Documents first serves the project.
CREATE TABLE documents.databases (
    project_number         bigint NOT NULL,
    -- '(default)', or a named database's id: 4 to 63 of a-z, 0-9 and '-', starting with a
    -- letter and ending with a letter or digit.
    database_id            text COLLATE "C" NOT NULL
                           CHECK (database_id = '(default)' OR database_id ~ '^[a-z][a-z0-9-]{2,61}[a-z0-9]$'),
    uid                    uuid NOT NULL,
    create_time            timestamptz NOT NULL,
    update_time            timestamptz NOT NULL,
    etag                   text NOT NULL,
    type                   text NOT NULL DEFAULT 'FIRESTORE_NATIVE' CHECK (type IN ('FIRESTORE_NATIVE', 'DATASTORE_MODE')),
    edition                text NOT NULL DEFAULT 'STANDARD' CHECK (edition IN ('STANDARD', 'ENTERPRISE')),
    location_id            text NOT NULL,
    concurrency_mode       text NOT NULL DEFAULT 'PESSIMISTIC'
                           CHECK (concurrency_mode IN ('OPTIMISTIC', 'PESSIMISTIC', 'OPTIMISTIC_WITH_ENTITY_GROUPS')),
    delete_protection      text NOT NULL DEFAULT 'DISABLED' CHECK (delete_protection IN ('DISABLED', 'ENABLED')),
    -- How far back read_time reaches; earliestVersionTime is computed from it, never stored.
    version_retention      interval NOT NULL DEFAULT '1 hour' CHECK (version_retention > interval '0'),
    point_in_time_recovery text NOT NULL DEFAULT 'DISABLED' CHECK (point_in_time_recovery IN ('DISABLED', 'ENABLED')),
    -- A database's own composite-index enforcement, NULL for the server's (ADR-025 §4.4).
    index_enforcement      text CHECK (index_enforcement IN ('enforce', 'warn', 'off')),
    PRIMARY KEY (project_number, database_id)
);

-- One row per database: the commit order (ADR-024 §2). Each commit's stamp statement takes the
-- row, adds one to seq and sets last_ts to clock_timestamp(), kept above the previous last_ts, and
-- holds it until COMMIT; so commits become visible in seq order, and seq <= the snapshot's seq
-- means committed and visible.
CREATE TABLE documents.sequencer (
    project_number  bigint NOT NULL,
    database_id     text COLLATE "C" NOT NULL,
    -- The last committed seq; contiguous, with no gap.
    seq             bigint NOT NULL DEFAULT 0 CHECK (seq >= 0),
    -- That commit's commit_time; the database's create_time before any commit.
    last_ts         timestamptz NOT NULL,
    -- Grows with each change to the set of indexes a commit writes (ADR-024 §2.5).
    catalog_version bigint NOT NULL DEFAULT 0 CHECK (catalog_version >= 0),
    PRIMARY KEY (project_number, database_id),
    FOREIGN KEY (project_number, database_id) REFERENCES documents.databases ON DELETE CASCADE
) WITH (fillfactor = 50); -- room for HOT updates of the hot row

-- One row per existing document (ADR-024 §1.2). A document that does not exist but has
-- descendants (a "missing" document, ListDocuments show_missing) has no row: it is found by a
-- skipping scan of name_key.
CREATE TABLE documents.documents (
    project_number bigint NOT NULL,
    database_id    text COLLATE "C" NOT NULL,
    -- The name's sort key, cut (see above).
    name_key       bytea NOT NULL CHECK (octet_length(name_key) BETWEEN 2 AND 2080),
    -- The name's sort key, whole.
    name           bytea NOT NULL,
    -- SHA-256 of the key of the document's collection (its parent's path and the collection
    -- id), and of the collection id: equality alone is asked of them, and both may be long.
    parent_hash    bytea NOT NULL CHECK (octet_length(parent_hash) = 32),
    cg_hash        bytea NOT NULL CHECK (octet_length(cg_hash) = 32),
    -- The parent document's name key, whole; empty for a top-level collection's document.
    parent         bytea NOT NULL,
    collection_id  text COLLATE "C" NOT NULL CHECK (octet_length(collection_id) BETWEEN 1 AND 1500),
    -- google.firestore.v1.Document with its fields alone, binary protobuf.
    doc            bytea NOT NULL,
    create_time    timestamptz NOT NULL,
    update_time    timestamptz NOT NULL CHECK (update_time >= create_time),
    -- The seq of the last commit that wrote the document: the version a commit checks.
    update_seq     bigint NOT NULL CHECK (update_seq > 0),
    -- Google's storage size (value.DocumentSize).
    size           integer NOT NULL CHECK (size > 0),
    -- From the collection's TTL policy, when it has one.
    expire_time    timestamptz,
    PRIMARY KEY (project_number, database_id, name_key)
);

-- A collection's documents in name order (ListDocuments, the reference path), and a collection
-- group's (ADR-025 §1). Rows are matched on parent and collection_id again after the hash.
CREATE INDEX documents_parent ON documents.documents (project_number, database_id, parent_hash, name_key);
CREATE INDEX documents_cg ON documents.documents (project_number, database_id, cg_hash, name_key);
CREATE INDEX documents_expire ON documents.documents (project_number, database_id, expire_time)
    WHERE expire_time IS NOT NULL;

-- One row per document image a commit replaced or deleted, for reads at a read_time and resumes
-- (ADR-024 §1.2, §3). A document's state at T: its documents row if update_time <= T, else the
-- versions row with begin_time <= T < end_time, else none.
CREATE TABLE documents.versions (
    project_number bigint NOT NULL,
    database_id    text COLLATE "C" NOT NULL,
    name_key       bytea NOT NULL CHECK (octet_length(name_key) BETWEEN 2 AND 2080),
    -- The seq of the commit that replaced or deleted the image.
    end_seq        bigint NOT NULL CHECK (end_seq > 0),
    name           bytea NOT NULL,
    -- As in documents, so that a collection is listed at T from this table too.
    parent_hash    bytea NOT NULL CHECK (octet_length(parent_hash) = 32),
    cg_hash        bytea NOT NULL CHECK (octet_length(cg_hash) = 32),
    parent         bytea NOT NULL,
    collection_id  text COLLATE "C" NOT NULL,
    -- The image's update_time, and the commit_time of end_seq.
    begin_time     timestamptz NOT NULL,
    end_time       timestamptz NOT NULL CHECK (end_time > begin_time),
    create_time    timestamptz NOT NULL,
    doc            bytea NOT NULL,
    PRIMARY KEY (project_number, database_id, name_key, end_seq)
);

CREATE INDEX versions_parent ON documents.versions (project_number, database_id, parent_hash, name_key);
CREATE INDEX versions_cg ON documents.versions (project_number, database_id, cg_hash, name_key);
-- Retention deletes by end_time; rows arrive in end_time order.
CREATE INDEX versions_end_time ON documents.versions USING brin (end_time);

-- Every commit's changes in seq order (ADR-024 §2.1, §4): the hub fills gaps and settles an
-- unknown outcome from it, listeners match targets on both images, and triggers can read it later.
-- before is the document as it was, with its create_time and update_time; after is the document
-- as written, with its create_time when it existed before: its update_time is commit_time, and so
-- is a new document's create_time. NULL is an absent document. A commit that changes nothing
-- writes no row and takes no seq.
CREATE TABLE documents.changelog (
    project_number bigint NOT NULL,
    database_id    text COLLATE "C" NOT NULL,
    seq            bigint NOT NULL CHECK (seq > 0),
    name_key       bytea NOT NULL CHECK (octet_length(name_key) BETWEEN 2 AND 2080),
    name           bytea NOT NULL,
    commit_time    timestamptz NOT NULL,
    before         bytea,
    after          bytea,
    -- The write's google.firestore.v1.DocumentMask as sent, when one write with a mask made it.
    update_mask    bytea,
    PRIMARY KEY (project_number, database_id, seq, name_key),
    CONSTRAINT changelog_changes CHECK (before IS NOT NULL OR after IS NOT NULL)
);

-- Composite indexes, and the single-field indexes automatic or configured (ADR-024 §1.2; ADR-025
-- §2, §3). Only READY indexes serve queries; commits write CREATING and READY ones (§2.5).
CREATE TABLE documents.indexes (
    project_number   bigint NOT NULL,
    database_id      text COLLATE "C" NOT NULL,
    id               bigint GENERATED ALWAYS AS IDENTITY,
    kind             text NOT NULL CHECK (kind IN ('composite', 'single_field')),
    collection_group text COLLATE "C" NOT NULL CHECK (octet_length(collection_group) BETWEEN 1 AND 1500),
    -- google.firestore.admin.v1.Index.QueryScope.
    query_scope      smallint NOT NULL CHECK (query_scope BETWEEN 1 AND 3),
    -- google.firestore.admin.v1.Index.fields as sent, vector_config with them, binary protobuf.
    spec             bytea NOT NULL,
    spec_hash        bytea NOT NULL CHECK (octet_length(spec_hash) = 32),
    state            text NOT NULL CHECK (state IN ('CREATING', 'READY', 'NEEDS_REPAIR', 'DELETING')),
    create_time      timestamptz NOT NULL,
    ready_time       timestamptz,
    -- The sort-key layout of its entries (value.KeyVersion).
    key_format       smallint NOT NULL DEFAULT 1 CHECK (key_format > 0),
    PRIMARY KEY (project_number, database_id, id),
    FOREIGN KEY (project_number, database_id) REFERENCES documents.databases ON DELETE CASCADE,
    CONSTRAINT indexes_spec_key UNIQUE (project_number, database_id, collection_group, query_scope, spec_hash),
    CONSTRAINT indexes_ready CHECK ((state = 'READY') = (ready_time IS NOT NULL) OR state IN ('NEEDS_REPAIR', 'DELETING'))
);

-- Field configuration: index overrides, exemptions and TTL policies (ADR-025 §2). '__default__'
-- and '*' are the database's default; '*' alone is a collection-wide exemption. Both names may
-- reach 1,500 bytes, so the key holds their hash (ADR-024 §1.4).
CREATE TABLE documents.field_overrides (
    project_number   bigint NOT NULL,
    database_id      text COLLATE "C" NOT NULL,
    -- SHA-256 of collection_group, a 0x00 byte and field_path.
    key_hash         bytea NOT NULL CHECK (octet_length(key_hash) = 32),
    collection_group text COLLATE "C" NOT NULL CHECK (octet_length(collection_group) BETWEEN 1 AND 1500),
    field_path       text COLLATE "C" NOT NULL CHECK (octet_length(field_path) BETWEEN 1 AND 1500),
    -- google.firestore.admin.v1.Field.IndexConfig as sent; NULL inherits.
    index_config     bytea,
    -- NULL without a TTL policy.
    ttl_state        text CHECK (ttl_state IN ('CREATING', 'ACTIVE')),
    update_time      timestamptz NOT NULL,
    etag             text NOT NULL,
    PRIMARY KEY (project_number, database_id, key_hash),
    FOREIGN KEY (project_number, database_id) REFERENCES documents.databases ON DELETE CASCADE
);

-- One row per index entry (ADR-024 §1.2): key is the entry's sort key, cut (see above): for a
-- COLLECTION-scope index the parent's key, then the values, then the name.
CREATE TABLE documents.index_entries (
    project_number bigint NOT NULL,
    database_id    text COLLATE "C" NOT NULL,
    index_id       bigint NOT NULL,
    key            bytea NOT NULL CHECK (octet_length(key) BETWEEN 2 AND 2080),
    -- The whole key when it is longer than 2,048 bytes, else NULL: key is the whole key.
    full_key       bytea CHECK (full_key IS NULL OR octet_length(full_key) > 2048),
    PRIMARY KEY (project_number, database_id, index_id, key),
    CONSTRAINT index_entries_cut CHECK ((full_key IS NULL) = (octet_length(key) <= 2048))
);

-- Usage for the console, upserted every few seconds outside any commit (ADR-024 §1.2).
CREATE TABLE documents.usage (
    project_number bigint NOT NULL,
    database_id    text COLLATE "C" NOT NULL,
    day            date NOT NULL,
    reads          bigint NOT NULL DEFAULT 0 CHECK (reads >= 0),
    writes         bigint NOT NULL DEFAULT 0 CHECK (writes >= 0),
    deletes        bigint NOT NULL DEFAULT 0 CHECK (deletes >= 0),
    stored_bytes   bigint NOT NULL DEFAULT 0,
    documents      bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (project_number, database_id, day)
);

-- The Admin API's long-running operations (ADR-025 §4.5).
CREATE TABLE documents.operations (
    project_number bigint NOT NULL,
    database_id    text COLLATE "C" NOT NULL,
    id             text COLLATE "C" NOT NULL CHECK (id ~ '^[A-Za-z0-9_-]{1,64}$'),
    kind           text NOT NULL,
    -- The operation's metadata and result (google.protobuf.Any), binary protobuf.
    metadata       bytea NOT NULL,
    done           boolean NOT NULL DEFAULT false,
    result         bytea,
    create_time    timestamptz NOT NULL,
    PRIMARY KEY (project_number, database_id, id),
    FOREIGN KEY (project_number, database_id) REFERENCES documents.databases ON DELETE CASCADE,
    CONSTRAINT operations_result CHECK (done OR result IS NULL)
);

-- child_skip returns the bound past the subtree of a child of a key prefix of plen bytes: name
-- is a whole name key below the prefix, so its next segment, at byte plen, names the child (a
-- numeric segment is 9 bytes; a text one ends at its 0x00 0x01). Every key below the child is
-- below the bound, every later child's above. A child whose prefix reaches 2,048 bytes is cut
-- there: its rows share their stored prefix, and the bound passes every key cut to that prefix.
-- The skipping scans of show_missing and ListCollectionIds step with it (ADR-024 §1.2).
-- +goose StatementBegin
CREATE FUNCTION documents.child_skip(name bytea, plen integer) RETURNS bytea
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
    SELECT CASE WHEN n >= 2048
        THEN substring(name FROM 1 FOR 2048) || '\xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'::bytea
        ELSE substring(name FROM 1 FOR n) || '\x03'::bytea END
    FROM (SELECT CASE get_byte(name, plen)
        WHEN 1 THEN plen + 9
        ELSE plen + position('\x0001'::bytea IN substring(name FROM plen + 2)) + 2 END AS n) AS child
$$;
-- +goose StatementEnd

-- conflict_unless fails the statement with serialization_failure when ok is false: the stamp
-- statement asserts with it that every document it writes is as the commit read it, so a
-- concurrent creation of the same document rolls the commit back and core retries it.
-- +goose StatementBegin
CREATE FUNCTION documents.conflict_unless(ok boolean) RETURNS boolean
LANGUAGE plpgsql AS $$
BEGIN
    IF ok IS NOT TRUE THEN
        RAISE EXCEPTION 'documents: a document changed under its commit' USING ERRCODE = 'serialization_failure';
    END IF;
    RETURN true;
END
$$;
-- +goose StatementEnd

-- +goose Down

DROP SCHEMA documents CASCADE;
