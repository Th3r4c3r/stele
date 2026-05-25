-- Down migration for local dev only. Never run in production.
DROP TRIGGER IF EXISTS events_no_delete ON events;
DROP TRIGGER IF EXISTS events_no_update ON events;
DROP FUNCTION IF EXISTS events_block_mutation();
DROP TABLE IF EXISTS events;
