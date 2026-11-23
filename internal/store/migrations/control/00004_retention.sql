-- +goose Up

-- Retention (housekeeping). Each purge reads the oldest rows of every account or project at
-- once, so each needs an index on the time it purges by. A console session ends at its absolute
-- expiry at the latest, revoked or idle; a CLI token at its expiry or its revocation.
CREATE INDEX audit_log_at_idx ON control.audit_log (at);
CREATE INDEX sessions_absolute_expires_idx ON control.sessions (absolute_expires_at);
CREATE INDEX access_tokens_expires_idx ON control.access_tokens (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX access_tokens_revoked_idx ON control.access_tokens (revoked_at) WHERE revoked_at IS NOT NULL;

-- The audit log stays append-only for what is recent: a row younger than seven days cannot be
-- deleted, by the server or by anyone else with its database role, so a compromised server
-- cannot erase the recent trail. Older rows may go, which is how retention
-- (retention.control_audit_days, at least 7) trims the table. UPDATE and TRUNCATE stay refused.
-- +goose StatementBegin
CREATE FUNCTION control.audit_log_keep_recent() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.at >= now() - interval '7 days' THEN
        RAISE EXCEPTION 'audit_log is append-only: rows younger than 7 days cannot be deleted'
            USING ERRCODE = 'insufficient_privilege';
    END IF;
    RETURN OLD;
END;
$$;
-- +goose StatementEnd

DROP TRIGGER audit_log_no_update ON control.audit_log;
CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON control.audit_log
    FOR EACH ROW EXECUTE FUNCTION control.audit_log_append_only();
CREATE TRIGGER audit_log_keep_recent BEFORE DELETE ON control.audit_log
    FOR EACH ROW EXECUTE FUNCTION control.audit_log_keep_recent();

-- +goose Down

DROP TRIGGER audit_log_keep_recent ON control.audit_log;
DROP TRIGGER audit_log_no_update ON control.audit_log;
CREATE TRIGGER audit_log_no_update BEFORE UPDATE OR DELETE ON control.audit_log
    FOR EACH ROW EXECUTE FUNCTION control.audit_log_append_only();
DROP FUNCTION control.audit_log_keep_recent();
DROP INDEX control.access_tokens_revoked_idx;
DROP INDEX control.access_tokens_expires_idx;
DROP INDEX control.sessions_absolute_expires_idx;
DROP INDEX control.audit_log_at_idx;
