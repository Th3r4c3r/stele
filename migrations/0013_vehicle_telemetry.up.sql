-- 0013_vehicle_telemetry: snapshot of newplat live telemetry per VIN.
-- See ADR-014.
--
-- One row per VIN, upserted on each sync. raw_payload is the full
-- newplat detail response (jsonb) so future projections can pick up
-- new fields without re-hitting the API. ON DELETE CASCADE so the
-- snapshot vanishes if the vehicle row goes away.

CREATE TABLE vehicle_telemetry (
    vin                text          PRIMARY KEY REFERENCES vehicles(vin) ON DELETE CASCADE,
    snapshot_at        timestamptz   NOT NULL DEFAULT now(),
    is_online          boolean       NOT NULL,
    imei               text          NULL,
    iccid              text          NULL,
    sim_end_time       timestamptz   NULL,
    agreement_end_time timestamptz   NULL,
    last_online_at     timestamptz   NULL,
    login_time         timestamptz   NULL,
    soc_pct            int           NULL,
    endurance_km       int           NULL,
    total_mileage_km   numeric(10,2) NULL,
    latitude           numeric(10,7) NULL,
    longitude          numeric(10,7) NULL,
    bms_temperature    int           NULL,
    gsm_signal         int           NULL,
    gps_satellites     int           NULL,
    fota_version       text          NULL,
    raw_payload        jsonb         NULL
);

CREATE INDEX vehicle_telemetry_snapshot_at_idx ON vehicle_telemetry (snapshot_at DESC);
CREATE INDEX vehicle_telemetry_online_idx      ON vehicle_telemetry (is_online) WHERE is_online = true;
-- SIM expiring soon: partial index keeps it cheap.
CREATE INDEX vehicle_telemetry_sim_expiring_idx
    ON vehicle_telemetry (sim_end_time)
    WHERE sim_end_time IS NOT NULL;
