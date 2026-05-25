-- 0008_case_number: human-readable identifier for cases (the UUID is
-- great for URLs and the event log, terrible for "call Mario about
-- case 9347"). The projector assigns the next value via
-- nextval('case_number_seq') on the first INSERT; subsequent applies
-- of the same CaseOpened event fall through ON CONFLICT (id) DO
-- NOTHING and preserve the original number.
--
-- The 200 existing rows are backfilled here in opened_at order so
-- their numbers correlate with chronology.

CREATE SEQUENCE IF NOT EXISTS case_number_seq START WITH 1 INCREMENT BY 1;

ALTER TABLE current_cases
    ADD COLUMN case_number bigint NULL;

-- Backfill in chronological order so number == age rank.
WITH ordered AS (
    SELECT id, ROW_NUMBER() OVER (ORDER BY opened_at, id) AS rn
    FROM current_cases
)
UPDATE current_cases c
   SET case_number = o.rn
  FROM ordered o
 WHERE c.id = o.id;

-- Advance the sequence past the highest backfilled value.
SELECT setval('case_number_seq',
              GREATEST(1, (SELECT COALESCE(MAX(case_number), 0) FROM current_cases)),
              true);

-- Now NOT NULL + UNIQUE.
ALTER TABLE current_cases
    ALTER COLUMN case_number SET NOT NULL,
    ADD CONSTRAINT current_cases_case_number_unique UNIQUE (case_number);
