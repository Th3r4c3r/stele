-- 0016_case_stage: repair-workflow stage per case.
--
-- Adds a second axis orthogonal to status:
--   status (triage / classified / closed)  = case management lifecycle
--   stage  (new / diagnosis / parts_ordered / parts_waiting / repair
--           / resolved)                    = repair workflow
--
-- A "classified warranty" case can be in any repair stage. Closing a
-- case transitions stage to 'resolved' (handled by projector). The
-- column is fed by the StageChanged event; stage_changed_at lets the
-- UI compute "N days in this stage" without a second query.
--
-- Backfill: existing closed cases land in 'resolved' with the close
-- timestamp; open cases start at 'new' (default).

ALTER TABLE current_cases
    ADD COLUMN IF NOT EXISTS stage             TEXT        NOT NULL DEFAULT 'new'
        CHECK (stage IN ('new','diagnosis','parts_ordered','parts_waiting','repair','resolved')),
    ADD COLUMN IF NOT EXISTS stage_changed_at  TIMESTAMPTZ NOT NULL DEFAULT now();

UPDATE current_cases
   SET stage            = 'resolved',
       stage_changed_at = COALESCE(closed_at, opened_at)
 WHERE status = 'closed';

CREATE INDEX IF NOT EXISTS current_cases_stage_idx ON current_cases (stage);
