-- 0009_pilot_masters: master data for the Vmoto pilot (ADR-013).
-- Three tables for vehicles + parts + the case_parts read model that
-- the PartReplaced/PartQuoted events feed.

CREATE TABLE vehicle_models (
    code         text         PRIMARY KEY,
    name         text         NOT NULL,
    generation   text         NULL,
    segment      text         NULL,
    capacity_kwh numeric(5,2) NULL,
    created_at   timestamptz  NOT NULL DEFAULT now()
);

CREATE TABLE vehicles (
    vin               text        PRIMARY KEY CHECK (length(vin) = 17),
    model_code        text        NOT NULL REFERENCES vehicle_models(code),
    manufactured_year int         NULL,
    sold_at           date        NULL,
    country           text        NULL,
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX vehicles_model_code_idx ON vehicles (model_code);

CREATE TABLE parts (
    pn             text          PRIMARY KEY,
    description    text          NOT NULL,
    category       text          NULL,
    price_eur      numeric(10,2) NULL,
    supersedes_pn  text          NULL REFERENCES parts(pn),
    created_at     timestamptz   NOT NULL DEFAULT now()
);

CREATE INDEX parts_category_idx ON parts (category) WHERE category IS NOT NULL;

-- case_parts is a read model fed by PartReplaced / PartQuoted events.
-- One row per event, so a case with 3 part rows = 3 events. cost_at_event
-- is computed at projection time as price_eur * qty (snapshot of price
-- when the event was recorded; price changes later do not retroactively
-- alter the case cost).
CREATE TABLE case_parts (
    id              uuid         PRIMARY KEY,
    case_id         uuid         NOT NULL,
    pn              text         NOT NULL,
    qty             int          NOT NULL CHECK (qty > 0),
    kind            text         NOT NULL CHECK (kind IN ('replaced', 'quoted')),
    cost_at_event   numeric(12,2) NOT NULL,
    recorded_at     timestamptz  NOT NULL,
    last_event_id   uuid         NOT NULL
);

CREATE INDEX case_parts_case_id_idx ON case_parts (case_id);
CREATE INDEX case_parts_pn_idx      ON case_parts (pn);
