DROP TABLE IF EXISTS vehicle_recalls;
ALTER TABLE vehicles
    DROP COLUMN IF EXISTS color,
    DROP COLUMN IF EXISTS controller_sn,
    DROP COLUMN IF EXISTS motor_sn,
    DROP COLUMN IF EXISTS battery1_sn,
    DROP COLUMN IF EXISTS battery2_sn;
