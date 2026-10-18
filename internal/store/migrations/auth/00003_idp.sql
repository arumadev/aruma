-- +goose Up

-- Sign-in with Google and Apple (T15, T20). A sign-in session is found by the state the
-- provider echoes back and is redeemed with the sessionId only the starting browser tab holds;
-- both are stored as HMACs. Rows are single-use and live ten minutes.
ALTER TABLE auth.idp_sessions
    ADD COLUMN state_hash        bytea NOT NULL,
    ADD COLUMN callback_url      text NOT NULL,             -- the redirect_uri sent to the provider
    ADD COLUMN code_verifier_enc bytea,                     -- PKCE verifier (envelope)
    ADD COLUMN auth_type         text NOT NULL DEFAULT '',  -- signInViaPopup, linkViaRedirect...
    ADD COLUMN app_name          text NOT NULL DEFAULT '',
    ADD COLUMN api_key           text NOT NULL DEFAULT '';
CREATE UNIQUE INDEX idp_sessions_state_key ON auth.idp_sessions (state_hash);
CREATE INDEX idp_sessions_expiry_idx ON auth.idp_sessions (expires_at);

-- Apple refresh tokens, kept only to revoke them when the account is deleted (App Store rule).
CREATE TABLE auth.idp_tokens (
    project_number    bigint NOT NULL,
    tenant_id         text NOT NULL DEFAULT '',
    uid               text NOT NULL,
    provider_id       text NOT NULL CHECK (provider_id IN ('apple.com')),
    refresh_token_enc bytea NOT NULL,                       -- envelope
    updated_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_number, tenant_id, uid, provider_id),
    FOREIGN KEY (project_number, tenant_id, uid, provider_id) REFERENCES auth.user_providers ON DELETE CASCADE
);

-- +goose Down

DROP TABLE auth.idp_tokens;
DROP INDEX auth.idp_sessions_expiry_idx;
DROP INDEX auth.idp_sessions_state_key;
ALTER TABLE auth.idp_sessions
    DROP COLUMN state_hash,
    DROP COLUMN callback_url,
    DROP COLUMN code_verifier_enc,
    DROP COLUMN auth_type,
    DROP COLUMN app_name,
    DROP COLUMN api_key;
