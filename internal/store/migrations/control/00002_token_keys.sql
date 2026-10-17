-- +goose Up

-- Each service account token remembers the key that signed its assertion, so deleting the key
-- revokes the tokens it bought instead of leaving them valid for up to an hour.
ALTER TABLE control.oauth_access_tokens
    ADD COLUMN key_id text REFERENCES control.service_account_keys (key_id) ON DELETE CASCADE;
CREATE INDEX oauth_access_tokens_key_id_idx ON control.oauth_access_tokens (key_id);

-- +goose Down

ALTER TABLE control.oauth_access_tokens DROP COLUMN key_id;
