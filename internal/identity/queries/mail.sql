-- name: CreateOobCode :exec
-- A code is dated no earlier than its user's revocation point, as a session is, so a code asked
-- for right after a password change is not taken for one from before it.
INSERT INTO auth.oob_codes (code_hash, project_number, tenant_id, uid, request_type, email, new_email, continue_url, expires_at, created_at)
VALUES (@code_hash, @project_number, @tenant_id, @uid, @request_type, @email, sqlc.narg(new_email), sqlc.narg(continue_url), @expires_at,
        GREATEST(now(), (SELECT u.tokens_valid_after FROM auth.users u
                         WHERE u.project_number = @project_number AND u.tenant_id = @tenant_id AND u.uid = @uid)));

-- name: GetOobCode :one
SELECT * FROM auth.oob_codes WHERE project_number = @project_number AND code_hash = @code_hash;

-- name: UseOobCode :one
-- Consumes a code once: a second use, even a concurrent one, finds no row.
UPDATE auth.oob_codes SET used_at = now()
WHERE project_number = @project_number AND code_hash = @code_hash AND used_at IS NULL AND expires_at > now()
RETURNING *;

-- name: RetireOobCodes :exec
-- Retires the user's other open codes of a type once one of them has been used.
UPDATE auth.oob_codes SET used_at = now()
WHERE project_number = @project_number AND tenant_id = @tenant_id AND uid = @uid
  AND request_type = @request_type AND used_at IS NULL;

-- name: PurgeOobCodes :execrows
-- scope: global — housekeeping across projects: codes a day past their use or expiry.
DELETE FROM auth.oob_codes
WHERE expires_at < now() - interval '1 day' OR used_at < now() - interval '1 day';

-- name: ListEmailTemplates :many
SELECT * FROM auth.email_templates WHERE project_number = @project_number ORDER BY kind, locale;

-- name: GetEmailTemplate :one
SELECT * FROM auth.email_templates WHERE project_number = @project_number AND kind = @kind AND locale = @locale;

-- name: UpsertEmailTemplate :one
INSERT INTO auth.email_templates (project_number, kind, locale, subject, body)
VALUES (@project_number, @kind, @locale, @subject, @body)
ON CONFLICT (project_number, kind, locale) DO UPDATE
SET subject = EXCLUDED.subject, body = EXCLUDED.body, updated_at = now()
RETURNING *;

-- name: DeleteEmailTemplate :exec
DELETE FROM auth.email_templates WHERE project_number = @project_number AND kind = @kind AND locale = @locale;

-- name: UpdateMailConfig :one
UPDATE auth.project_config SET
    mail_sender_name = @mail_sender_name,
    mail_sender_address = @mail_sender_address,
    mail_reply_to = @mail_reply_to,
    smtp_host = @smtp_host,
    smtp_port = @smtp_port,
    smtp_username = @smtp_username,
    smtp_password_enc = @smtp_password_enc,
    smtp_tls = @smtp_tls,
    mail_dev_mailbox = @mail_dev_mailbox,
    updated_at = now()
WHERE project_number = @project_number
RETURNING *;

-- name: NextOutboxID :one
-- scope: global — draws the next message id, which the sealed content is bound to; reads no rows.
SELECT nextval(pg_get_serial_sequence('auth.email_outbox', 'id'))::bigint;

-- name: InsertOutbox :exec
INSERT INTO auth.email_outbox (id, project_number, kind, recipient, locale, message_enc, dev_mailbox, sent_at)
OVERRIDING SYSTEM VALUE
VALUES (@id, @project_number, @kind, @recipient, @locale, @message_enc, @dev_mailbox, sqlc.narg(sent_at));

-- name: ClaimOutbox :many
-- scope: global — the delivery worker serves every project. A claimed row is leased for five
-- minutes, so a worker that dies mid-send leaves it to be retried, not lost.
UPDATE auth.email_outbox SET attempts = attempts + 1, next_attempt_at = now() + interval '5 minutes'
WHERE id IN (
    SELECT id FROM auth.email_outbox
    WHERE sent_at IS NULL AND failed_at IS NULL AND NOT dev_mailbox AND next_attempt_at <= now()
    ORDER BY next_attempt_at
    LIMIT @batch
    FOR UPDATE SKIP LOCKED
)
RETURNING id, project_number, kind, recipient, locale, message_enc, attempts;

-- name: MarkOutboxSent :exec
UPDATE auth.email_outbox SET sent_at = now(), last_error = NULL
WHERE project_number = @project_number AND id = @id;

-- name: MarkOutboxRetry :exec
UPDATE auth.email_outbox SET last_error = @last_error, next_attempt_at = @next_attempt_at,
    failed_at = CASE WHEN @give_up::boolean THEN now() END
WHERE project_number = @project_number AND id = @id;

-- name: ListMailbox :many
SELECT id, kind, recipient, locale, message_enc, created_at FROM auth.email_outbox
WHERE project_number = @project_number AND dev_mailbox
ORDER BY id DESC
LIMIT @max_messages;

-- name: ClearMailbox :execrows
DELETE FROM auth.email_outbox WHERE project_number = @project_number AND dev_mailbox;

-- name: PurgeOutbox :execrows
-- scope: global — housekeeping across projects: messages delivered or abandoned 30 days ago.
DELETE FROM auth.email_outbox
WHERE sent_at < now() - interval '30 days' OR failed_at < now() - interval '30 days';
