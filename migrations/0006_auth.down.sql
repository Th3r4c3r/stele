DROP TABLE IF EXISTS password_reset_tokens;
DROP TABLE IF EXISTS sessions;
ALTER TABLE users
    DROP COLUMN IF EXISTS deactivated_at,
    DROP COLUMN IF EXISTS password_hash;
ALTER TABLE users ALTER COLUMN role DROP DEFAULT;
