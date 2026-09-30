-- Initial Database Schema for Sudux

-- Enable UUID extension if needed
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";

-- 1. Game Modes (e.g. cell_race, time, survival)
CREATE TABLE IF NOT EXISTS game_modes (
    id SERIAL PRIMARY KEY,
    key VARCHAR(50) UNIQUE NOT NULL,
    name VARCHAR(100) NOT NULL,
    description TEXT,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO game_modes (key, name, description) VALUES
('cell_race', 'Cell Race', 'Solve the most cells to win. Correct moves build your combo score.')
ON CONFLICT (key) DO NOTHING;

-- 2. Match Formats (e.g. 1v1, 1_team_vs_bot, 2v2, ffa)
CREATE TABLE IF NOT EXISTS match_formats (
    id SERIAL PRIMARY KEY,
    key VARCHAR(50) UNIQUE NOT NULL,
    name VARCHAR(100) NOT NULL,
    description TEXT,
    max_teams INT NOT NULL DEFAULT 2,
    max_players_per_team INT NOT NULL DEFAULT 1,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

INSERT INTO match_formats (key, name, description, max_teams, max_players_per_team) VALUES
('1v1', '1 vs 1', 'Two players compete on the same board.', 2, 1),
('1_team_vs_bot', 'Team vs Bot', 'Cooperate against a bot opponent.', 2, 1),
('2v2', '2 vs 2', 'Two teams of two players compete on the same board.', 2, 2),
('ffa', 'Free For All', 'Multiple players compete individually on the same board.', 4, 1)
ON CONFLICT (key) DO NOTHING;

-- 3. Players
CREATE TABLE IF NOT EXISTS players (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    email VARCHAR(255) UNIQUE NOT NULL,
    name VARCHAR(50) NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    last_seen_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_players_email ON players(email);

-- 4. Refresh Tokens
CREATE TABLE IF NOT EXISTS refresh_tokens (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    player_id UUID NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    token_hash VARCHAR(64) NOT NULL UNIQUE,
    jti VARCHAR(255) NOT NULL UNIQUE,
    expires_at TIMESTAMP WITH TIME ZONE NOT NULL,
    revoked BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_refresh_tokens_player ON refresh_tokens(player_id);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_jti ON refresh_tokens(jti);

-- 5. Friendships
CREATE TABLE IF NOT EXISTS friendships (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    requester_id UUID NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    addressee_id UUID NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    status VARCHAR(20) NOT NULL CHECK (status IN ('pending', 'accepted', 'rejected', 'blocked')),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT chk_friendship_self CHECK (requester_id <> addressee_id)
);

-- Unique constraint ensuring requester_id < addressee_id ordering or unique pair
CREATE UNIQUE INDEX IF NOT EXISTS idx_unique_friendship_pair
ON friendships (LEAST(requester_id, addressee_id), GREATEST(requester_id, addressee_id));

CREATE INDEX IF NOT EXISTS idx_friendships_requester ON friendships(requester_id);
CREATE INDEX IF NOT EXISTS idx_friendships_addressee ON friendships(addressee_id);

-- 6. Matches
CREATE TABLE IF NOT EXISTS matches (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    game_mode_id INT NOT NULL REFERENCES game_modes(id),
    match_format_id INT NOT NULL REFERENCES match_formats(id),
    status VARCHAR(20) NOT NULL CHECK (status IN ('waiting', 'starting', 'playing', 'finished', 'cancelled')),
    difficulty VARCHAR(20) NOT NULL DEFAULT 'medium',
    puzzle_json JSONB NOT NULL,
    solution_json JSONB NOT NULL,
    board_state_json JSONB NOT NULL,
    winning_team_id UUID NULL,
    started_at TIMESTAMP WITH TIME ZONE NULL,
    ended_at TIMESTAMP WITH TIME ZONE NULL,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_matches_status ON matches(status);

-- 7. Match Teams
CREATE TABLE IF NOT EXISTS match_teams (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    match_id UUID NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    team_number INT NOT NULL,
    is_bot_team BOOLEAN NOT NULL DEFAULT FALSE,
    bot_difficulty VARCHAR(20) NULL,
    score INT NOT NULL DEFAULT 0,
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_match_team_number UNIQUE (match_id, team_number)
);

CREATE INDEX IF NOT EXISTS idx_match_teams_match ON match_teams(match_id);

-- Foreign key for winning_team_id on matches
ALTER TABLE matches ADD CONSTRAINT fk_matches_winning_team FOREIGN KEY (winning_team_id) REFERENCES match_teams(id) ON DELETE SET NULL;

-- 8. Match Participants
CREATE TABLE IF NOT EXISTS match_participants (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    match_id UUID NOT NULL REFERENCES matches(id) ON DELETE CASCADE,
    team_id UUID NOT NULL REFERENCES match_teams(id) ON DELETE CASCADE,
    player_id UUID NULL REFERENCES players(id) ON DELETE CASCADE, -- Nullable for Bot participant
    is_bot BOOLEAN NOT NULL DEFAULT FALSE,
    slot_no INT NOT NULL,
    score INT NOT NULL DEFAULT 0,
    solved_cells INT NOT NULL DEFAULT 0,
    solved_time_seconds INT NULL,
    result VARCHAR(20) NULL CHECK (result IN ('win', 'loss', 'draw')),
    created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_match_participant_slot UNIQUE (match_id, slot_no)
);

CREATE INDEX IF NOT EXISTS idx_match_participants_match ON match_participants(match_id);
CREATE INDEX IF NOT EXISTS idx_match_participants_player ON match_participants(player_id);

-- 9. Stats
CREATE TABLE IF NOT EXISTS stats (
    id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
    player_id UUID NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    game_mode_id INT NOT NULL REFERENCES game_modes(id),
    match_format_id INT NOT NULL REFERENCES match_formats(id),
    matches_played INT NOT NULL DEFAULT 0,
    wins INT NOT NULL DEFAULT 0,
    losses INT NOT NULL DEFAULT 0,
    draws INT NOT NULL DEFAULT 0,
    best_score INT NOT NULL DEFAULT 0,
    best_time_seconds INT NULL,
    total_solved_cells INT NOT NULL DEFAULT 0,
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_player_mode_format UNIQUE (player_id, game_mode_id, match_format_id)
);

CREATE INDEX IF NOT EXISTS idx_stats_player ON stats(player_id);
