-- +goose Up

-- A queued message carries a live action link, so its content is sealed like the other secrets
-- (ADR-010; AAD "auth.email_outbox|message|<id>"): a dump of the outbox yields no usable code.
-- Nothing wrote the outbox before this migration, so the plaintext columns hold no rows.
DELETE FROM auth.email_outbox;
ALTER TABLE auth.email_outbox
    DROP COLUMN subject,
    DROP COLUMN body,
    ADD COLUMN locale      text NOT NULL DEFAULT 'ar' CHECK (locale IN ('ar', 'en')),
    ADD COLUMN message_enc bytea NOT NULL,              -- sealed {subject, text, html, link}
    ADD COLUMN dev_mailbox boolean NOT NULL DEFAULT false, -- kept for the console's dev mailbox, never sent
    ADD COLUMN failed_at   timestamptz;                 -- delivery abandoned after the last retry
CREATE INDEX email_outbox_mailbox_idx ON auth.email_outbox (project_number, id DESC) WHERE dev_mailbox;

-- The project's choice of dev mailbox. NULL follows the instance: the dev mailbox when the
-- instance allows it (mail.dev_mailbox) and the project has no SMTP host.
ALTER TABLE auth.project_config ADD COLUMN mail_dev_mailbox boolean;

-- +goose Down

ALTER TABLE auth.project_config DROP COLUMN mail_dev_mailbox;
DROP INDEX auth.email_outbox_mailbox_idx;
DELETE FROM auth.email_outbox;
ALTER TABLE auth.email_outbox
    DROP COLUMN failed_at,
    DROP COLUMN dev_mailbox,
    DROP COLUMN message_enc,
    DROP COLUMN locale,
    ADD COLUMN subject text NOT NULL,
    ADD COLUMN body text NOT NULL;
