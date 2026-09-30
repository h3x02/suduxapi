DROP INDEX IF EXISTS idx_matches_deadline;
ALTER TABLE matches DROP COLUMN IF EXISTS deadline_at;
