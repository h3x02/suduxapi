-- Add server-side match deadline so stalled/abandoned matches can be finalized
-- by the game server / matchmaker janitor.

ALTER TABLE matches ADD COLUMN IF NOT EXISTS deadline_at TIMESTAMP WITH TIME ZONE NULL;

CREATE INDEX IF NOT EXISTS idx_matches_deadline
    ON matches(deadline_at)
    WHERE status IN ('waiting', 'starting', 'playing');
