-- 0006_auth: server-side sessions + password hash + reset tokens + soft delete.
-- See docs/adr/0009-auth-admin.md.

ALTER TABLE users
    ADD COLUMN password_hash   text        NULL,
    ADD COLUMN deactivated_at  timestamptz NULL;

-- Set role default to 'ops' for new users; existing rows already have
-- a role from the seeder.
ALTER TABLE users
    ALTER COLUMN role SET DEFAULT 'ops';

CREATE TABLE sessions (
    id              uuid        PRIMARY KEY,
    user_id         uuid        NOT NULL REFERENCES users(id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    ip              text        NOT NULL DEFAULT '',
    user_agent_hash text        NOT NULL DEFAULT ''
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE password_reset_tokens (
    token_hash text        PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users(id),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX password_reset_tokens_user_id_idx ON password_reset_tokens (user_id);
