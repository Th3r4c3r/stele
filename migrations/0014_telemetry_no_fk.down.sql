-- Re-add the FK. WARNING: this fails if vehicle_telemetry contains
-- rows whose vin is not present in vehicles. Down migrations are
-- best-effort here; the prod path is forward-only.
ALTER TABLE vehicle_telemetry
    ADD CONSTRAINT vehicle_telemetry_vin_fkey
    FOREIGN KEY (vin) REFERENCES vehicles(vin) ON DELETE CASCADE;
