-- +goose Up

CREATE SCHEMA IF NOT EXISTS storage;

-- Cloud Storage for Firebase (ADR-022 §3). A project has one bucket in v0.3, so the project
-- number names it: no bucket id appears in these keys. Object bytes live on the local disk under
-- data_dir (ADR-022 §4); these rows say which files are visible. A file key never derives from
-- an object name: p/<project_number>/<uuid> for objects, s/<project_number>/<uuid> for staging.

-- One row per live object. name is opaque UTF-8 text compared byte by byte (COLLATE "C"), as GCS
-- orders its listings: no NFC normalisation, no case folding (ADR-022 §3).
CREATE TABLE storage.objects (
    project_number      bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE,
    name                text COLLATE "C" NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 1024),
    -- Microseconds of the clock, and always above the name's previous generation (ADR-022 §12).
    generation          bigint NOT NULL CHECK (generation > 0),
    metageneration      bigint NOT NULL DEFAULT 1 CHECK (metageneration > 0),
    -- A new key for every generation, so a write never replaces a file a reader holds open.
    blob_key            text NOT NULL CHECK (blob_key ~ '^p/[0-9]+/[0-9a-f-]{36}$'),
    size                bigint NOT NULL CHECK (size >= 0),
    md5                 bytea NOT NULL CHECK (octet_length(md5) = 16),
    crc32c              bigint NOT NULL CHECK (crc32c BETWEEN 0 AND 4294967295), -- Castagnoli
    -- The five content fields, NULL when unset; they become response headers, so the service
    -- refuses CR, LF and control characters before they reach a row (ADR-022 §17).
    content_type        text,
    content_encoding    text,
    content_disposition text,
    content_language    text,
    cache_control       text,
    -- Custom metadata, a JSON object of strings. It never holds firebaseStorageDownloadTokens.
    metadata            jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(metadata) = 'object'),
    -- The download tokens, sealed by the KEK with (project_number, name) as associated data, so
    -- a copy of the database holds no usable token (ADR-010 S-15, ADR-022 §9). NULL when none.
    download_tokens     bytea,
    time_created        timestamptz NOT NULL,
    updated             timestamptz NOT NULL,
    PRIMARY KEY (project_number, name),
    CONSTRAINT objects_blob_key_key UNIQUE (blob_key)
);

-- One row per resumable upload session (ADR-022 §6, §7). The session URL's upload_id is 128
-- random bits; only its HMAC is stored (crypto.Keys.HashToken).
CREATE TABLE storage.upload_sessions (
    id_hash           bytea PRIMARY KEY CHECK (octet_length(id_hash) = 32),
    project_number    bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE,
    name              text COLLATE "C" NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 1024),
    -- The surface that started it: firebase (/v0), gcs, or console (a session the dashboard
    -- issues, uploaded over /v0, with a one-hour life and a declared size, ADR-022 §15).
    surface           text NOT NULL CHECK (surface IN ('firebase', 'gcs', 'console')),
    state             text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'final', 'cancelled')),
    -- The identity bound at start (ADR-022 §7): who the rules see at finalize.
    principal_kind    text NOT NULL CHECK (principal_kind IN ('anonymous', 'user', 'admin')),
    -- uid, the verified token's claims, auth_time and iat for a user; the service account or
    -- console account for an admin. Claims, never a token.
    principal         jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(principal) = 'object'),
    -- The client address at start, for the sessions-per-IP limit (IPv6 counted per /64).
    client_ip         inet,
    -- NULL when the client declared no size (Android).
    declared_size     bigint CHECK (declared_size >= 0),
    -- The incoming metadata after StripClientTokens, the preconditions and the client's
    -- checksums (GCS), as core encodes them.
    attrs             jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(attrs) = 'object'),
    preconditions     jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(preconditions) = 'object'),
    checksums         jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(checksums) = 'object'),
    -- The durable bytes announced to the client; the staging file may be longer after a crash
    -- and is cut back to it (ADR-022 §6.3). hash_state is the crc32c and md5 state at received.
    received          bigint NOT NULL DEFAULT 0 CHECK (received >= 0),
    hash_state        bytea,
    staging_key       text NOT NULL CHECK (staging_key ~ '^s/[0-9]+/[0-9a-f-]{36}$'),
    -- Written before the staging file moves to it, so a finalize after a crash finds it.
    blob_key          text CHECK (blob_key ~ '^p/[0-9]+/[0-9a-f-]{36}$'),
    -- A final session answers every repeat with its outcome: the generation it committed, or
    -- the core error it was refused with (its code and message), never an HTTP status, which
    -- each surface maps for itself.
    final_status      text,
    final_body        text,
    result_generation bigint,
    -- Quota held for bytes on their way in (ADR-022 §6.5).
    reserved_bytes    bigint NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0),
    created_at        timestamptz NOT NULL DEFAULT now(),
    -- The last durable byte, for the idle expiry.
    progressed_at     timestamptz NOT NULL DEFAULT now(),
    expires_at        timestamptz NOT NULL,
    CONSTRAINT upload_sessions_staging_key_key UNIQUE (staging_key),
    CONSTRAINT upload_sessions_blob_key_key UNIQUE (blob_key),
    CONSTRAINT upload_sessions_outcome CHECK (state = 'final' OR (final_status IS NULL AND result_generation IS NULL))
);
CREATE INDEX upload_sessions_project_idx ON storage.upload_sessions (project_number) WHERE state = 'active';
CREATE INDEX upload_sessions_expires_idx ON storage.upload_sessions (expires_at);

-- The deletion outbox: every file no row points to any more, queued in the transaction that
-- dropped the reference, and deleted after gc_grace (ADR-022 §5).
CREATE TABLE storage.blob_gc (
    blob_key       text PRIMARY KEY CHECK (blob_key ~ '^(p|s)/[0-9]+/[0-9a-f-]{36}$'),
    project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE,
    queued_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX blob_gc_queued_at_idx ON storage.blob_gc (queued_at);

-- A project's quota account, created by upsert on the first reservation (ADR-022 §3, §6.5).
CREATE TABLE storage.usage (
    project_number           bigint PRIMARY KEY REFERENCES control.projects (project_number) ON DELETE CASCADE,
    used_bytes               bigint NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
    reserved_bytes           bigint NOT NULL DEFAULT 0 CHECK (reserved_bytes >= 0),
    -- The part of reserved_bytes held by sessions without an identity, capped by
    -- anonymous_staged_fraction.
    anonymous_reserved_bytes bigint NOT NULL DEFAULT 0 CHECK (anonymous_reserved_bytes >= 0),
    object_count             bigint NOT NULL DEFAULT 0 CHECK (object_count >= 0),
    CONSTRAINT usage_anonymous_within_reserved CHECK (anonymous_reserved_bytes <= reserved_bytes)
);

-- The bucket's writable settings: its CORS rules, with GCS's cors[] meaning (ADR-022 §11). A
-- row exists once the settings were first changed; none means no CORS for alt=media.
CREATE TABLE storage.buckets (
    project_number bigint PRIMARY KEY REFERENCES control.projects (project_number) ON DELETE CASCADE,
    cors           jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(cors) = 'array'),
    metageneration bigint NOT NULL DEFAULT 1 CHECK (metageneration > 0),
    updated        timestamptz NOT NULL DEFAULT now()
);

-- One row, belonging to no project: the id that binds this database to its data_dir, whose
-- INSTANCE file holds the same id (ADR-022 §5.1).
CREATE TABLE storage.instance (
    id         uuid PRIMARY KEY,
    one_row    boolean NOT NULL DEFAULT true CHECK (one_row),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT instance_one_row_key UNIQUE (one_row)
);

-- +goose Down

DROP SCHEMA storage CASCADE;
