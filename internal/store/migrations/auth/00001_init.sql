-- +goose Up

CREATE SCHEMA IF NOT EXISTS auth;

-- Every key starts with project_number, then tenant_id ('' until tenants exist), per ADR-009.

-- Per-project Auth settings (one row per project, created on first use).
CREATE TABLE auth.project_config (
    project_number          bigint PRIMARY KEY,
    password_enabled        boolean NOT NULL DEFAULT true,
    anonymous_enabled       boolean NOT NULL DEFAULT false,
    -- Users may create accounts from client SDKs; off means only the Admin API creates them.
    client_signup_enabled   boolean NOT NULL DEFAULT true,
    enumeration_protection  boolean NOT NULL DEFAULT true,
    authorized_domains      text[] NOT NULL DEFAULT '{localhost}',
    password_min_length     int NOT NULL DEFAULT 6 CHECK (password_min_length BETWEEN 6 AND 30),
    password_require_lower  boolean NOT NULL DEFAULT false,
    password_require_upper  boolean NOT NULL DEFAULT false,
    password_require_digit  boolean NOT NULL DEFAULT false,
    password_require_symbol boolean NOT NULL DEFAULT false,
    password_enforcement    text NOT NULL DEFAULT 'OFF' CHECK (password_enforcement IN ('OFF', 'ENFORCE')),
    app_name                text NOT NULL DEFAULT '',
    mail_sender_name        text NOT NULL DEFAULT '',
    mail_sender_address     text NOT NULL DEFAULT '',
    mail_reply_to           text NOT NULL DEFAULT '',
    smtp_host               text NOT NULL DEFAULT '',
    smtp_port               int NOT NULL DEFAULT 587,
    smtp_username           text NOT NULL DEFAULT '',
    smtp_password_enc       bytea,          -- envelope-encrypted (ADR-010)
    smtp_tls                text NOT NULL DEFAULT 'starttls' CHECK (smtp_tls IN ('starttls', 'tls', 'none')),
    updated_at              timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE auth.users (
    project_number     bigint NOT NULL,
    tenant_id          text NOT NULL DEFAULT '',
    uid                text NOT NULL CHECK (length(uid) BETWEEN 1 AND 128),
    email              text,
    email_lower        text GENERATED ALWAYS AS (lower(email)) STORED,
    email_verified     boolean NOT NULL DEFAULT false,
    display_name       text,
    photo_url          text,
    disabled           boolean NOT NULL DEFAULT false,
    custom_claims      jsonb CHECK (custom_claims IS NULL OR octet_length(custom_claims::text) <= 1000),
    tokens_valid_after timestamptz NOT NULL DEFAULT now(),
    created_at         timestamptz NOT NULL DEFAULT now(),
    last_login_at      timestamptz,
    last_refresh_at    timestamptz,
    PRIMARY KEY (project_number, tenant_id, uid)
);
CREATE UNIQUE INDEX users_email_key ON auth.users (project_number, tenant_id, email_lower) WHERE email IS NOT NULL;

-- Kept apart from users so imported hashes (scrypt from Firebase, later) fit without migration.
CREATE TABLE auth.user_passwords (
    project_number bigint NOT NULL,
    tenant_id      text NOT NULL DEFAULT '',
    uid            text NOT NULL,
    algorithm      text NOT NULL CHECK (algorithm IN ('argon2id')),
    hash           text NOT NULL,         -- PHC string
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_number, tenant_id, uid),
    FOREIGN KEY (project_number, tenant_id, uid) REFERENCES auth.users ON DELETE CASCADE
);

CREATE TABLE auth.user_providers (
    project_number bigint NOT NULL,
    tenant_id      text NOT NULL DEFAULT '',
    uid            text NOT NULL,
    provider_id    text NOT NULL CHECK (provider_id IN ('password', 'google.com', 'apple.com')),
    raw_id         text NOT NULL,
    email          text,
    display_name   text,
    photo_url      text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_number, tenant_id, uid, provider_id),
    UNIQUE (project_number, tenant_id, provider_id, raw_id),
    FOREIGN KEY (project_number, tenant_id, uid) REFERENCES auth.users ON DELETE CASCADE
);

-- Rows outlive their user on purpose: a refresh after deletion or revocation must answer
-- USER_NOT_FOUND or TOKEN_EXPIRED, which make the client SDKs sign out, rather than
-- INVALID_REFRESH_TOKEN, which leaves them stuck. Housekeeping removes them later.
CREATE TABLE auth.refresh_tokens (
    token_hash       bytea PRIMARY KEY,   -- HMAC; the token itself is never stored
    project_number   bigint NOT NULL,
    tenant_id        text NOT NULL DEFAULT '',
    uid              text NOT NULL,
    sign_in_provider text NOT NULL,
    custom_claims    jsonb,               -- claims carried by a custom-token sign-in
    auth_time        timestamptz NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_used_at     timestamptz
);
CREATE INDEX refresh_tokens_user_idx ON auth.refresh_tokens (project_number, tenant_id, uid);

CREATE TABLE auth.oob_codes (
    code_hash      bytea PRIMARY KEY,
    project_number bigint NOT NULL,
    tenant_id      text NOT NULL DEFAULT '',
    uid            text,
    request_type   text NOT NULL CHECK (request_type IN ('PASSWORD_RESET', 'VERIFY_EMAIL', 'VERIFY_AND_CHANGE_EMAIL', 'RECOVER_EMAIL')),
    email          text NOT NULL,
    new_email      text,
    continue_url   text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL,
    used_at        timestamptz
);
CREATE INDEX oob_codes_expiry_idx ON auth.oob_codes (expires_at);

CREATE TABLE auth.idp_sessions (
    session_hash   bytea PRIMARY KEY,     -- binds state and sessionId
    project_number bigint NOT NULL,
    tenant_id      text NOT NULL DEFAULT '',
    provider_id    text NOT NULL,
    nonce          text NOT NULL,
    redirect_url   text NOT NULL,
    event_id       text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL,
    used_at        timestamptz
);

CREATE TABLE auth.idp_configs (
    project_number   bigint NOT NULL,
    provider_id      text NOT NULL CHECK (provider_id IN ('google.com', 'apple.com')),
    enabled          boolean NOT NULL DEFAULT false,
    client_id        text NOT NULL DEFAULT '',
    client_secret_enc bytea,              -- Google client secret (envelope)
    apple_team_id    text NOT NULL DEFAULT '',
    apple_key_id     text NOT NULL DEFAULT '',
    apple_private_key_enc bytea,          -- Apple .p8 (envelope)
    updated_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_number, provider_id)
);

CREATE TABLE auth.signing_keys (
    kid            text PRIMARY KEY CHECK (kid ~ '^[0-9a-f]{40}$'),
    project_number bigint NOT NULL,
    purpose        text NOT NULL CHECK (purpose IN ('id_token', 'session_cookie')),
    state          text NOT NULL CHECK (state IN ('next', 'active', 'retired', 'destroyed')),
    private_key_enc bytea,                -- envelope; NULL once destroyed
    public_jwk     jsonb NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    activated_at   timestamptz,
    retired_at     timestamptz
);
CREATE UNIQUE INDEX signing_keys_one_active ON auth.signing_keys (project_number, purpose) WHERE state = 'active';
CREATE INDEX signing_keys_project_idx ON auth.signing_keys (project_number, purpose, state);

CREATE TABLE auth.email_templates (
    project_number bigint NOT NULL,
    kind           text NOT NULL CHECK (kind IN ('VERIFY_EMAIL', 'PASSWORD_RESET', 'VERIFY_AND_CHANGE_EMAIL', 'RECOVER_EMAIL')),
    locale         text NOT NULL CHECK (locale IN ('ar', 'en')),
    subject        text NOT NULL,
    body           text NOT NULL,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_number, kind, locale)
);

-- Written in the same transaction as the change that needs the mail; a worker delivers it.
CREATE TABLE auth.email_outbox (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_number  bigint NOT NULL,
    kind            text NOT NULL,
    recipient       text NOT NULL,
    subject         text NOT NULL,
    body            text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    attempts        int NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz,
    last_error      text
);
CREATE INDEX email_outbox_pending_idx ON auth.email_outbox (next_attempt_at) WHERE sent_at IS NULL;
CREATE INDEX email_outbox_project_idx ON auth.email_outbox (project_number, id DESC);

-- Sign-in events, revocations and configuration changes (separate from control.audit_log).
CREATE TABLE auth.audit (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_number bigint NOT NULL,
    tenant_id      text NOT NULL DEFAULT '',
    at             timestamptz NOT NULL DEFAULT now(),
    uid            text,
    event          text NOT NULL,
    result         text NOT NULL CHECK (result IN ('ok', 'denied', 'error')),
    ip             inet,
    details        jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX audit_project_at_idx ON auth.audit (project_number, at DESC);

-- +goose Down

DROP SCHEMA auth CASCADE;
