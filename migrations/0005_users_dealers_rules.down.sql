DROP INDEX IF EXISTS current_cases_assignee_idx;
ALTER TABLE current_cases DROP COLUMN IF EXISTS assignee_id;
DROP TABLE IF EXISTS assignment_rules;
DROP TABLE IF EXISTS dealers;
DROP TABLE IF EXISTS users;
-- citext extension intentionally NOT dropped (other tables may use it).
