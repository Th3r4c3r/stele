DROP INDEX IF EXISTS current_cases_stage_idx;
ALTER TABLE current_cases
    DROP COLUMN IF EXISTS stage_changed_at,
    DROP COLUMN IF EXISTS stage;
