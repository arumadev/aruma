-- +goose Up

-- Security rules for Cloud Storage and Firestore (ADR-021 §7, ADR-022 §2). The control plane owns
-- them, not a service: tenancy compiles, stores and publishes them, and the services read the
-- compiled release through it. Isolation is the rest of control's (ADR-017): project_number on
-- every row, and a release points only at a ruleset of its own project.

-- A ruleset is a saved rules source, never changed once saved: projects/{p}/rulesets/{ruleset_id}.
-- files is [{"name": ..., "content": ...}], one file in v0.3 as the Rules API takes one.
CREATE TABLE control.rulesets (
    project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE,
    ruleset_id     uuid NOT NULL DEFAULT gen_random_uuid(),
    service        text NOT NULL CHECK (service IN ('cloud.firestore', 'firebase.storage')),
    files          jsonb NOT NULL CHECK (jsonb_typeof(files) = 'array'),
    create_time    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_number, ruleset_id),
    CONSTRAINT rulesets_ruleset_id_key UNIQUE (ruleset_id)
);
CREATE INDEX rulesets_project_create_time_idx ON control.rulesets (project_number, create_time DESC, ruleset_id);

-- A release points a name at a ruleset of the same project; publishing moves the pointer. The
-- Storage release is stored under the logical name firebase.storage/default whatever the bucket
-- is called today, so a change of project_domain keeps it (ADR-022 §2). A ruleset a release
-- points to cannot be deleted.
CREATE TABLE control.rules_releases (
    project_number bigint NOT NULL REFERENCES control.projects (project_number) ON DELETE CASCADE,
    name           text NOT NULL CHECK (name <> '' AND octet_length(name) <= 256),
    ruleset_id     uuid NOT NULL,
    create_time    timestamptz NOT NULL DEFAULT now(),
    update_time    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (project_number, name),
    CONSTRAINT rules_releases_ruleset_fkey FOREIGN KEY (project_number, ruleset_id)
        REFERENCES control.rulesets (project_number, ruleset_id) ON DELETE RESTRICT
);
CREATE INDEX rules_releases_ruleset_idx ON control.rules_releases (project_number, ruleset_id);

-- +goose Down

DROP TABLE control.rules_releases;
DROP TABLE control.rulesets;
