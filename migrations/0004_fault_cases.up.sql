-- 0004_fault_cases: replace the warranty-only read model with a
-- triage/classify/close model. See docs/adr/0007-fault-case-refactor.md.

DROP TABLE IF EXISTS current_claims;

CREATE TABLE current_cases (
    id            uuid        PRIMARY KEY,
    status        text        NOT NULL CHECK (status IN ('triage', 'classified', 'closed')),
    kind          text        NULL CHECK (kind IS NULL OR kind IN (
                                  'warranty', 'out_of_warranty', 'goodwill',
                                  'recall', 'unrelated', 'customer_education')),
    dealer        text        NOT NULL,
    vin           text        NOT NULL,
    fault_code    text        NOT NULL,
    description   text        NOT NULL,
    opened_at     timestamptz NOT NULL,
    classified_at timestamptz NULL,
    closed_at     timestamptz NULL,
    last_update   timestamptz NOT NULL,
    note_count    int         NOT NULL DEFAULT 0,
    last_event_id uuid        NOT NULL
);

CREATE INDEX current_cases_status_opened_idx
    ON current_cases (status, opened_at DESC);

CREATE INDEX current_cases_kind_idx
    ON current_cases (kind)
    WHERE kind IS NOT NULL;

CREATE INDEX current_cases_dealer_idx
    ON current_cases (dealer);
