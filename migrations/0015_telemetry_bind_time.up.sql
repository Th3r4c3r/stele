-- 0015_telemetry_bind_time: add bind_time to vehicle_telemetry.
--
-- Maps to newplat pojo.createTime, which represents when the bike↔
-- user binding was established in newplat (proxy for "when the
-- customer activated the bike"). Distinct from device.createTime
-- (device registered by manufacturer) and from agreement_end_time
-- (service-contract expiry).
--
-- Nullable: many VINs in newplat lack a populated pojo block.
ALTER TABLE vehicle_telemetry
    ADD COLUMN IF NOT EXISTS bind_time timestamptz NULL;
