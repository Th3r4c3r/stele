-- 0010_parts_notes: add optional notes field to parts master.
-- Used on the case-detail add-part form to surface supersession /
-- variant / legacy info to the operator when they pick a PN.
-- Additive only: existing rows stay NULL until the CSV is re-imported
-- with a `notes` column.
ALTER TABLE parts ADD COLUMN IF NOT EXISTS notes TEXT NULL;
