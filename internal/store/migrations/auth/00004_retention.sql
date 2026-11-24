-- +goose Up

-- Retention (housekeeping). The purge of auth.audit (retention.auth_audit_days) reads the
-- oldest rows of every project at once, so it needs the time alone as an index.
CREATE INDEX audit_at_idx ON auth.audit (at);

-- A refresh token that stopped working, because its user was deleted or its session revoked,
-- stays for 30 days so that a client refreshing it hears USER_NOT_FOUND or TOKEN_EXPIRED, which
-- make the SDKs sign out, rather than INVALID_REFRESH_TOKEN, which leaves them stuck (00001).
-- ended_at dates that moment, and housekeeping deletes the row 30 days after it. Before, the 30
-- days ran from the token's creation, so a session older than a month that was revoked or whose
-- user was deleted lost its row within the hour. Two triggers set ended_at on every path that
-- ends a session, so no caller can forget it: deleting the user, and moving tokens_valid_after
-- past the token's creation, compared to the second as the refresh compares them.
ALTER TABLE auth.refresh_tokens ADD COLUMN ended_at timestamptz;
CREATE INDEX refresh_tokens_ended_idx ON auth.refresh_tokens (ended_at) WHERE ended_at IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION auth.refresh_tokens_end_on_user_delete() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE auth.refresh_tokens SET ended_at = now()
    WHERE project_number = OLD.project_number AND tenant_id = OLD.tenant_id AND uid = OLD.uid
      AND ended_at IS NULL;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION auth.refresh_tokens_end_on_revoke() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE auth.refresh_tokens SET ended_at = now()
    WHERE project_number = NEW.project_number AND tenant_id = NEW.tenant_id AND uid = NEW.uid
      AND ended_at IS NULL
      AND date_trunc('second', created_at) < date_trunc('second', NEW.tokens_valid_after);
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER users_end_refresh_tokens_on_delete AFTER DELETE ON auth.users
    FOR EACH ROW EXECUTE FUNCTION auth.refresh_tokens_end_on_user_delete();
CREATE TRIGGER users_end_refresh_tokens_on_revoke AFTER UPDATE OF tokens_valid_after ON auth.users
    FOR EACH ROW WHEN (NEW.tokens_valid_after > OLD.tokens_valid_after)
    EXECUTE FUNCTION auth.refresh_tokens_end_on_revoke();

-- The tokens that stopped working before this migration: their 30 days start now.
UPDATE auth.refresh_tokens r SET ended_at = now()
WHERE NOT EXISTS (
    SELECT 1 FROM auth.users u
    WHERE u.project_number = r.project_number AND u.tenant_id = r.tenant_id AND u.uid = r.uid
      AND u.created_at <= r.created_at
      AND date_trunc('second', u.tokens_valid_after) <= date_trunc('second', r.created_at));

-- +goose Down

DROP TRIGGER users_end_refresh_tokens_on_revoke ON auth.users;
DROP TRIGGER users_end_refresh_tokens_on_delete ON auth.users;
DROP FUNCTION auth.refresh_tokens_end_on_revoke();
DROP FUNCTION auth.refresh_tokens_end_on_user_delete();
DROP INDEX auth.refresh_tokens_ended_idx;
ALTER TABLE auth.refresh_tokens DROP COLUMN ended_at;
DROP INDEX auth.audit_at_idx;
