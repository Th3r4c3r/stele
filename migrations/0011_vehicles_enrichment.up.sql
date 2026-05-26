-- 0011_vehicles_enrichment: enrich vehicles master with the fields
-- present on the Vmoto VIN list (color + four serial-number tracking
-- fields) and split recalls into their own multi-valued table.
--
-- Recalls are normalised so we can query "how many VINs are subject
-- to recall VRC003" or "is this VIN affected by any recall" with
-- regular joins instead of scanning a denormalised array column.

ALTER TABLE vehicles
    ADD COLUMN IF NOT EXISTS color          text NULL,
    ADD COLUMN IF NOT EXISTS controller_sn  text NULL,
    ADD COLUMN IF NOT EXISTS motor_sn       text NULL,
    ADD COLUMN IF NOT EXISTS battery1_sn    text NULL,
    ADD COLUMN IF NOT EXISTS battery2_sn    text NULL;

CREATE TABLE IF NOT EXISTS vehicle_recalls (
    vin         text        NOT NULL REFERENCES vehicles(vin) ON DELETE CASCADE,
    recall_code text        NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (vin, recall_code)
);

CREATE INDEX IF NOT EXISTS vehicle_recalls_code_idx
    ON vehicle_recalls (recall_code);
