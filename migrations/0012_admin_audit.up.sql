-- 0012_admin_audit: append-only audit log for /admin/* mutations.
--
-- Captures every successful (2xx/3xx) POST to /admin/*. Insertion is
-- driven by web/AuditAdminActions middleware; handlers enrich each
-- row with a human-readable summary via audit.SetSummary(ctx, "...").
--
-- actor_email is denormalised on purpose: if a user gets deactivated
-- or renamed later, the audit row still shows who did what at the
-- time. The FK on actor_user_id is intentionally absent: this is a
-- log, not a relational view, and we accept stale references.

CREATE TABLE admin_audit (
    id           uuid        PRIMARY KEY,
    at           timestamptz NOT NULL DEFAULT now(),
    actor_user_id uuid       NULL,
    actor_email  text        NOT NULL,
    method       text        NOT NULL,
    path         text        NOT NULL,
    status       int         NOT NULL,
    summary      text        NULL,
    ip           text        NULL,
    user_agent   text        NULL
);

CREATE INDEX admin_audit_at_idx     ON admin_audit (at DESC);
CREATE INDEX admin_audit_actor_idx  ON admin_audit (actor_user_id, at DESC);
