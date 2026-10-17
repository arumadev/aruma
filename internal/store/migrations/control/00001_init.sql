-- +goose Up

CREATE SCHEMA IF NOT EXISTS control;

-- Project owners. All accounts are equal: no instance admin, each sees only its own projects (U-19, U-20).
CREATE TABLE control.accounts (
    id            uuid PRIMARY KEY DEFAULT uuidv7(),
    email         text NOT NULL,
    email_lower   text NOT NULL GENERATED ALWAYS AS (lower(email)) STORED,
    password_hash text NOT NULL, -- argon2id, PHC string
    display_name  text NOT NULL DEFAULT '',
    locale        text NOT NULL DEFAULT 'ar' CHECK (locale IN ('ar', 'en')),
    disabled      boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT accounts_email_lower_key UNIQUE (email_lower)
);

-- Console sessions. Only the HMAC of the session token is stored; the CSRF token is derived
-- from the session token, so it needs no column.
CREATE TABLE control.sessions (
    id                  uuid PRIMARY KEY DEFAULT uuidv7(),
    account_id          uuid NOT NULL REFERENCES control.accounts (id) ON DELETE CASCADE,
    token_hash          bytea NOT NULL,
    ip                  inet,
    user_agent          text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    last_seen_at        timestamptz NOT NULL DEFAULT now(),
    idle_expires_at     timestamptz NOT NULL,
    absolute_expires_at timestamptz NOT NULL,
    revoked_at          timestamptz,
    CONSTRAINT sessions_token_hash_key UNIQUE (token_hash)
);
CREATE INDEX sessions_account_id_idx ON control.sessions (account_id);

-- Personal CLI tokens. The plaintext is shown once at creation.
CREATE TABLE control.access_tokens (
    id           uuid PRIMARY KEY DEFAULT uuidv7(),
    account_id   uuid NOT NULL REFERENCES control.accounts (id) ON DELETE CASCADE,
    name         text NOT NULL,
    prefix       text NOT NULL, -- shown in the console to tell tokens apart
    token_hash   bytea NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz,
    CONSTRAINT access_tokens_token_hash_key UNIQUE (token_hash)
);
CREATE INDEX access_tokens_account_id_idx ON control.access_tokens (account_id);

-- One owner per project. project_id is never reused, so deleted rows stay.
CREATE TABLE control.projects (
    project_number   bigint PRIMARY KEY
        CHECK (project_number BETWEEN 100000000000 AND 999999999999),
    project_id       text NOT NULL
        CHECK (project_id ~ '^[a-z][a-z0-9-]{4,28}[a-z0-9]$'),
    display_name     text NOT NULL,
    owner_account_id uuid NOT NULL REFERENCES control.accounts (id) ON DELETE RESTRICT,
    state            text NOT NULL DEFAULT 'ACTIVE' CHECK (state IN ('ACTIVE', 'DELETED')),
    etag             text NOT NULL,
    auth_domain      text NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    delete_time      timestamptz,
    expire_time      timestamptz,
    CONSTRAINT projects_project_id_key UNIQUE (project_id),
    CONSTRAINT projects_deleted_times CHECK ((state = 'DELETED') = (delete_time IS NOT NULL))
);
CREATE INDEX projects_owner_account_id_idx ON control.projects (owner_account_id);

-- API keys identify a project to the client SDKs. They are not secrets.
CREATE TABLE control.api_keys (
    uid            uuid PRIMARY KEY DEFAULT uuidv7(),
    project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE,
    key_string     text NOT NULL
        CHECK (key_string ~ '^A[A-Za-z0-9_-]{38}$' AND key_string NOT LIKE 'AIza%'),
    display_name   text NOT NULL DEFAULT '',
    restrictions   jsonb NOT NULL DEFAULT '{}',
    state          text NOT NULL DEFAULT 'ACTIVE' CHECK (state IN ('ACTIVE', 'DISABLED', 'DELETED')),
    rotated_from   uuid REFERENCES control.api_keys (uid) ON DELETE SET NULL,
    etag           text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT api_keys_key_string_key UNIQUE (key_string)
);
CREATE INDEX api_keys_project_number_idx ON control.api_keys (project_number);

CREATE TABLE control.apps (
    app_id         text PRIMARY KEY
        CHECK (app_id ~ '^1:[0-9]{12}:(web|ios|android):[0-9a-f]+$'),
    project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE,
    platform       text NOT NULL CHECK (platform IN ('WEB', 'IOS', 'ANDROID')),
    display_name   text NOT NULL DEFAULT '',
    package_name   text,
    bundle_id      text,
    api_key_id     uuid REFERENCES control.api_keys (uid) ON DELETE SET NULL,
    state          text NOT NULL DEFAULT 'ACTIVE' CHECK (state IN ('ACTIVE', 'DELETED')),
    etag           text NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    delete_time    timestamptz,
    expire_time    timestamptz,
    CONSTRAINT apps_platform_ids CHECK (
        (platform = 'WEB'     AND package_name IS NULL     AND bundle_id IS NULL) OR
        (platform = 'IOS'     AND package_name IS NULL     AND bundle_id IS NOT NULL) OR
        (platform = 'ANDROID' AND package_name IS NOT NULL AND bundle_id IS NULL)
    ),
    CONSTRAINT apps_project_package_key UNIQUE (project_number, package_name),
    CONSTRAINT apps_project_bundle_key UNIQUE (project_number, bundle_id)
);

-- package_name and bundle_id are fixed once the app exists.
-- +goose StatementBegin
CREATE FUNCTION control.apps_freeze_ids() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.package_name IS DISTINCT FROM OLD.package_name
       OR NEW.bundle_id IS DISTINCT FROM OLD.bundle_id
       OR NEW.platform <> OLD.platform
       OR NEW.project_number <> OLD.project_number THEN
        RAISE EXCEPTION 'app identifiers are immutable' USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER apps_freeze_ids BEFORE UPDATE ON control.apps
    FOR EACH ROW EXECUTE FUNCTION control.apps_freeze_ids();

-- Android only; enforced by the service layer.
CREATE TABLE control.app_sha_certificates (
    id         uuid PRIMARY KEY DEFAULT uuidv7(),
    app_id     text NOT NULL REFERENCES control.apps (app_id) ON DELETE CASCADE,
    sha_hash   text NOT NULL CHECK (sha_hash ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    cert_type  text NOT NULL CHECK (cert_type IN ('SHA_1', 'SHA_256')),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT app_sha_certificates_app_hash_key UNIQUE (app_id, sha_hash),
    CONSTRAINT app_sha_certificates_length CHECK (
        (cert_type = 'SHA_1' AND length(sha_hash) = 40) OR
        (cert_type = 'SHA_256' AND length(sha_hash) = 64)
    )
);

CREATE TABLE control.service_accounts (
    id             uuid PRIMARY KEY DEFAULT uuidv7(),
    project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE,
    email          text NOT NULL,
    unique_id      text NOT NULL CHECK (unique_id ~ '^[0-9]{21}$'),
    display_name   text NOT NULL DEFAULT '',
    disabled       boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT service_accounts_email_key UNIQUE (email),
    CONSTRAINT service_accounts_unique_id_key UNIQUE (unique_id)
);
CREATE INDEX service_accounts_project_number_idx ON control.service_accounts (project_number);

-- Public keys only. The private key is handed to the developer once and never stored.
CREATE TABLE control.service_account_keys (
    key_id             text PRIMARY KEY CHECK (key_id ~ '^[0-9a-f]{40}$'),
    service_account_id uuid NOT NULL REFERENCES control.service_accounts (id) ON DELETE CASCADE,
    public_key_pem     text NOT NULL,
    algorithm          text NOT NULL DEFAULT 'RS256' CHECK (algorithm = 'RS256'),
    valid_after        timestamptz NOT NULL DEFAULT now(),
    valid_before       timestamptz,
    disabled           boolean NOT NULL DEFAULT false,
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX service_account_keys_service_account_id_idx ON control.service_account_keys (service_account_id);

-- One-hour access tokens minted for service accounts.
CREATE TABLE control.oauth_access_tokens (
    token_hash     bytea PRIMARY KEY,
    principal_id   uuid NOT NULL REFERENCES control.service_accounts (id) ON DELETE CASCADE,
    project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE,
    scopes         text[] NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL
);
CREATE INDEX oauth_access_tokens_expires_at_idx ON control.oauth_access_tokens (expires_at);

-- Administrative actions on projects. Append-only; no foreign keys so entries outlive what they describe.
CREATE TABLE control.audit_log (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at             timestamptz NOT NULL DEFAULT now(),
    actor_type     text NOT NULL CHECK (actor_type IN ('account', 'service_account', 'system')),
    actor_id       uuid,
    project_number bigint,
    action         text NOT NULL,
    target         text,
    result         text NOT NULL CHECK (result IN ('ok', 'denied', 'error')),
    ip             inet,
    details        jsonb NOT NULL DEFAULT '{}' -- never secrets
);
CREATE INDEX audit_log_project_at_idx ON control.audit_log (project_number, at DESC);
CREATE INDEX audit_log_actor_at_idx ON control.audit_log (actor_id, at DESC);

-- +goose StatementBegin
CREATE FUNCTION control.audit_log_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only' USING ERRCODE = 'insufficient_privilege';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE ON control.audit_log
    FOR EACH ROW EXECUTE FUNCTION control.audit_log_append_only();
CREATE TRIGGER audit_log_no_truncate BEFORE TRUNCATE ON control.audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION control.audit_log_append_only();

-- +goose Down

DROP SCHEMA control CASCADE;
