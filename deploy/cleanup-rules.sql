-- Deactivate duplicate assignment_rules accumulated across re-seeds.
-- Two rules are "duplicate" if they share the same (name, priority,
-- assignee_id) triple. We keep only the most recently created copy
-- of each triple; older copies get active = false.
--
-- Idempotent: running twice does nothing on the second pass.
--
-- See docs/adr/0012-dashboard-cleanup.md D7.

WITH ranked AS (
    SELECT id,
           ROW_NUMBER() OVER (
             PARTITION BY name, priority, assignee_id
             ORDER BY created_at DESC
           ) AS rk
    FROM assignment_rules
)
UPDATE assignment_rules r
   SET active = false
  FROM ranked
 WHERE r.id = ranked.id
   AND ranked.rk > 1
   AND r.active = true;
