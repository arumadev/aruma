-- +goose Up

-- The single-instance lock's fence (ADR-024 §7): a commit asks, under the sequencer row its stamp
-- took, whether the session that took the lock for this process still holds it, and fails before
-- COMMIT when it does not. holder is that session's backend pid and holder_start its
-- backend_start, which tells it from a later session given the same pid. The lock is an int4-pair
-- advisory lock: classid is the first key (as oid), objid the second, objsubid 2. The SQLSTATE is
-- Aruma's own, so that core tells it from any other failure (core.ErrNotServing).
-- +goose StatementBegin
CREATE FUNCTION documents.instance_fence(holder integer, holder_start timestamptz) RETURNS void
LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_locks
            WHERE locktype = 'advisory' AND granted AND pid = holder
                AND classid = hashtext('documents.instance')::oid AND objid = 0 AND objsubid = 2)
        OR (SELECT backend_start FROM pg_stat_get_activity(holder)) IS DISTINCT FROM holder_start THEN
        RAISE EXCEPTION 'documents: this process no longer holds the single-instance lock' USING ERRCODE = 'AR001';
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down

DROP FUNCTION documents.instance_fence(integer, timestamptz);
