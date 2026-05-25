-- 0003_warranty: read model for the warranty Claim aggregate.
-- See docs/adr/0005-warranty-domain.md D5.

CREATE TABLE current_claims (
    id            uuid        PRIMARY KEY,
    status        text        NOT NULL CHECK (status IN ('open', 'closed')),
    dealer        text        NOT NULL,
    vin           text        NOT NULL,
    fault_code    text        NOT NULL,
    description   text        NOT NULL,
    opened_at     timestamptz NOT NULL,
    closed_at     timestamptz NULL,
    last_update   timestamptz NOT NULL,
    note_count    int         NOT NULL DEFAULT 0,
    last_event_id uuid        NOT NULL
);

CREATE INDEX current_claims_status_opened_idx
    ON current_claims (status, opened_at DESC);

CREATE INDEX current_claims_dealer_idx
    ON current_claims (dealer);
