ALTER TABLE current_cases DROP CONSTRAINT IF EXISTS current_cases_case_number_unique;
ALTER TABLE current_cases DROP COLUMN IF EXISTS case_number;
DROP SEQUENCE IF EXISTS case_number_seq;
