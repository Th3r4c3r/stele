-- 0002_projection_cursors: per-projector consumer position.
-- See docs/adr/0004-projection-engine.md D2.

CREATE TABLE projection_cursors (
    name             text        PRIMARY KEY,
    last_recorded_at timestamptz NOT NULL,
    last_event_id    uuid        NOT NULL,
    updated_at       timestamptz NOT NULL DEFAULT now()
);

-- Example M1b projection: count of events by (aggregate_type, type).
-- Each row also tracks the latest event id it has incorporated, so a
-- replay (Apply called twice on the same event) is a no-op for that row.
CREATE TABLE projection_event_counts (
    aggregate_type text  NOT NULL,
    type           text  NOT NULL,
    count          bigint NOT NULL,
    last_event_id  uuid   NOT NULL,
    PRIMARY KEY (aggregate_type, type)
);
