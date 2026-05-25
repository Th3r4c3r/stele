-- 0005: master data for the multi-user prep (ADR-008).

CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
    id              uuid        PRIMARY KEY,
    email           citext      UNIQUE NOT NULL,
    name            text        NOT NULL,
    role            text        NOT NULL,
    region          text        NULL,
    specializations text[]      NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE dealers (
    code       text        PRIMARY KEY,
    name       text        NOT NULL,
    region     text        NOT NULL,
    country    text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE assignment_rules (
    id                  uuid        PRIMARY KEY,
    name                text        NOT NULL,
    priority            int         NOT NULL,
    match_fault_prefix  text        NULL,
    match_dealer_region text        NULL,
    assignee_id         uuid        NOT NULL REFERENCES users(id),
    active              bool        NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX assignment_rules_priority_active_idx
    ON assignment_rules (priority)
    WHERE active;

-- Read model extension: assignment lives on current_cases.
-- FK constraint not added (read-model rebuild via replay could race
-- with a user deletion; we treat the FK as soft at projection level).
ALTER TABLE current_cases
    ADD COLUMN assignee_id uuid NULL;

CREATE INDEX current_cases_assignee_idx
    ON current_cases (assignee_id)
    WHERE assignee_id IS NOT NULL;
