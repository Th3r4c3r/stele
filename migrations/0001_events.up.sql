-- 0001_events: append-only event log with bi-temporal columns.
-- See docs/adr/0003-event-store.md for rationale.

CREATE TABLE events (
    id             uuid        PRIMARY KEY,
    aggregate_type text        NOT NULL,
    aggregate_id   uuid        NOT NULL,
    type           text        NOT NULL,
    payload        jsonb       NOT NULL,
    occurred_at    timestamptz NOT NULL,
    recorded_at    timestamptz NOT NULL DEFAULT now(),
    recorded_by    text        NOT NULL DEFAULT 'system'
);

CREATE INDEX events_aggregate_occurred_idx
    ON events (aggregate_id, occurred_at);

CREATE INDEX events_type_occurred_idx
    ON events (aggregate_type, occurred_at);

CREATE INDEX events_recorded_brin
    ON events USING brin (recorded_at);

-- Append-only enforcement: reject UPDATE/DELETE from any role except
-- the redactor (which does not exist yet, so all mutations are blocked).
CREATE OR REPLACE FUNCTION events_block_mutation() RETURNS trigger AS $$
BEGIN
    IF current_user <> 'stele_redactor' THEN
        RAISE EXCEPTION 'events is append-only (current_user=%)', current_user;
    END IF;
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER events_no_update
    BEFORE UPDATE ON events
    FOR EACH ROW EXECUTE FUNCTION events_block_mutation();

CREATE TRIGGER events_no_delete
    BEFORE DELETE ON events
    FOR EACH ROW EXECUTE FUNCTION events_block_mutation();
