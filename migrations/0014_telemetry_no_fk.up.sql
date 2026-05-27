-- 0014_telemetry_no_fk: relax vehicle_telemetry.vin FK on vehicles.
--
-- The FK from 0013 was too strict: it blocked snapshots for VINs not
-- yet present in the vehicles master (demo VINs, VINs from cases
-- opened on unknown bikes, fresh fleet additions). Telemetry is a
-- read-only cache of an external source; an "orphan" row is fine and
-- a "missing master" gap is something /admin/telemetry can surface
-- as a hint to import the VIN, rather than a hard reject at insert.
--
-- The vin stays PRIMARY KEY (one snapshot per VIN). We just drop the
-- referential integrity hook.

ALTER TABLE vehicle_telemetry
    DROP CONSTRAINT IF EXISTS vehicle_telemetry_vin_fkey;
