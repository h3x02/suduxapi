package match

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/h3x02/suduxapi/internal/bot"
	"github.com/h3x02/suduxapi/internal/postgres"
	"github.com/h3x02/suduxapi/internal/puzzle"
	"github.com/h3x02/suduxapi/internal/stats"
)

type Status string

const (
	StatusWaiting   Status = "waiting"
	StatusStarting  Status = "starting"
	StatusPlaying   Status = "playing"
	StatusFinished  Status = "finished"
	StatusCancelled Status = "cancelled"
)

type Match struct {
	ID            uuid.UUID         `json:"id"`
	GameModeID    int               `json:"game_mode_id"`
	MatchFormatID int               `json:"match_format_id"`
	Status        Status            `json:"status"`
	Difficulty    puzzle.Difficulty `json:"difficulty"`
	PuzzleBoard   [81]int           `json:"puzzle_board"`
	Solution      [81]int           `json:"solution"`
	BoardState    [81]int           `json:"board_state"`
	CellOwners    [81]string        `json:"cell_owners"` // player_id or bot_id string
	WinningTeamID *uuid.UUID        `json:"winning_team_id,omitempty"`
	StartedAt     *time.Time        `json:"started_at,omitempty"`
	EndedAt       *time.Time        `json:"ended_at,omitempty"`
	DeadlineAt    *time.Time        `json:"deadline_at,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	Teams         []*MatchTeam      `json:"teams"`
}

type MatchTeam struct {
	ID            uuid.UUID           `json:"id"`
	MatchID       uuid.UUID           `json:"match_id"`
	TeamNumber    int                 `json:"team_number"`
	IsBotTeam     bool                `json:"is_bot_team"`
	BotDifficulty *bot.Difficulty     `json:"bot_difficulty,omitempty"`
	Score         int                 `json:"score"`
	Participants  []*MatchParticipant `json:"participants"`
}

type MatchParticipant struct {
	ID                uuid.UUID  `json:"id"`
	MatchID           uuid.UUID  `json:"match_id"`
	TeamID            uuid.UUID  `json:"team_id"`
	PlayerID          *uuid.UUID `json:"player_id,omitempty"`
	IsBot             bool       `json:"is_bot"`
	SlotNo            int        `json:"slot_no"`
	Score             int        `json:"score"`
	Combo             int        `json:"combo"`
	SolvedCells       int        `json:"solved_cells"`
	SolvedTimeSeconds *int       `json:"solved_time_seconds,omitempty"`
	Result            *string    `json:"result,omitempty"`
}

type Repository struct {
	db        *postgres.DB
	statsRepo *stats.Repository
}

func NewRepository(db *postgres.DB) *Repository {
	return &Repository{
		db:        db,
		statsRepo: stats.NewRepository(db),
	}
}

// CreateMatchWithTeams creates the match, its teams and participants, sets it to
// playing with a deadline — all in a single transaction, so a crash can never
// leave a half-created match that players are locked into.
func (r *Repository) CreateMatchWithTeams(
	ctx context.Context,
	gameModeKey, matchFormatKey string,
	diff puzzle.Difficulty,
	pz *puzzle.Puzzle,
	teams []*TeamSpec,
	deadline time.Time,
) (*Match, error) {
	var gameModeID, matchFormatID int
	err := r.db.Pool.QueryRow(ctx, `SELECT id FROM game_modes WHERE key = $1`, gameModeKey).Scan(&gameModeID)
	if err != nil {
		return nil, fmt.Errorf("invalid game mode: %w", err)
	}
	err = r.db.Pool.QueryRow(ctx, `SELECT id FROM match_formats WHERE key = $1`, matchFormatKey).Scan(&matchFormatID)
	if err != nil {
		return nil, fmt.Errorf("invalid match format: %w", err)
	}

	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	puzzleJSON, err := json.Marshal(pz.Board)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal puzzle: %w", err)
	}
	solutionJSON, err := json.Marshal(pz.Solution)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal solution: %w", err)
	}
	boardStateJSON, err := json.Marshal(pz.Board)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal board state: %w", err)
	}

	m := &Match{
		GameModeID:    gameModeID,
		MatchFormatID: matchFormatID,
		Status:        StatusPlaying,
		Difficulty:    diff,
		PuzzleBoard:   pz.Board,
		Solution:      pz.Solution,
		BoardState:    pz.Board,
		DeadlineAt:    &deadline,
	}

	err = tx.QueryRow(ctx,
		`INSERT INTO matches (game_mode_id, match_format_id, status, difficulty, puzzle_json, solution_json, board_state_json, started_at, deadline_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, CURRENT_TIMESTAMP, $8)
		 RETURNING id, created_at`,
		gameModeID, matchFormatID, string(StatusPlaying), diff, puzzleJSON, solutionJSON, boardStateJSON, deadline,
	).Scan(&m.ID, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to insert match: %w", err)
	}

	for _, ts := range teams {
		t := &MatchTeam{
			MatchID:       m.ID,
			TeamNumber:    ts.TeamNumber,
			IsBotTeam:     ts.IsBotTeam,
			BotDifficulty: ts.BotDifficulty,
		}
		err = tx.QueryRow(ctx,
			`INSERT INTO match_teams (match_id, team_number, is_bot_team, bot_difficulty)
			 VALUES ($1, $2, $3, $4)
			 RETURNING id, match_id, team_number, is_bot_team, bot_difficulty, score`,
			m.ID, ts.TeamNumber, ts.IsBotTeam, ts.BotDifficulty,
		).Scan(&t.ID, &t.MatchID, &t.TeamNumber, &t.IsBotTeam, &t.BotDifficulty, &t.Score)
		if err != nil {
			return nil, fmt.Errorf("failed to add team %d: %w", ts.TeamNumber, err)
		}

		for _, ps := range ts.Participants {
			p := &MatchParticipant{
				MatchID:  m.ID,
				TeamID:   t.ID,
				PlayerID: ps.PlayerID,
				IsBot:    ps.IsBot,
				SlotNo:   ps.SlotNo,
			}
			err = tx.QueryRow(ctx,
				`INSERT INTO match_participants (match_id, team_id, player_id, is_bot, slot_no)
				 VALUES ($1, $2, $3, $4, $5)
				 RETURNING id, match_id, team_id, player_id, is_bot, slot_no, score, solved_cells`,
				m.ID, t.ID, ps.PlayerID, ps.IsBot, ps.SlotNo,
			).Scan(&p.ID, &p.MatchID, &p.TeamID, &p.PlayerID, &p.IsBot, &p.SlotNo, &p.Score, &p.SolvedCells)
			if err != nil {
				return nil, fmt.Errorf("failed to add participant (slot %d): %w", ps.SlotNo, err)
			}
			t.Participants = append(t.Participants, p)
		}
		m.Teams = append(m.Teams, t)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("failed to commit match creation: %w", err)
	}
	return m, nil
}

// TeamSpec / ParticipantSpec describe a match layout for CreateMatchWithTeams.
type TeamSpec struct {
	TeamNumber   int
	IsBotTeam    bool
	BotDifficulty *bot.Difficulty
	Participants []ParticipantSpec
}

type ParticipantSpec struct {
	PlayerID *uuid.UUID
	IsBot    bool
	SlotNo   int
}

func (r *Repository) GetActiveMatchByPlayer(ctx context.Context, playerID uuid.UUID) (*Match, error) {
	query := `
		SELECT m.id, m.game_mode_id, m.match_format_id, m.status, m.difficulty, m.puzzle_json, m.solution_json, m.board_state_json, m.winning_team_id, m.started_at, m.ended_at, m.deadline_at, m.created_at
		FROM matches m
		JOIN match_participants mp ON m.id = mp.match_id
		WHERE mp.player_id = $1 AND m.status IN ('waiting', 'starting', 'playing')
		ORDER BY m.created_at DESC
		LIMIT 1
	`
	var m Match
	var pzJSON, solJSON, bsJSON []byte
	err := r.db.Pool.QueryRow(ctx, query, playerID).Scan(&m.ID, &m.GameModeID, &m.MatchFormatID, &m.Status, &m.Difficulty, &pzJSON, &solJSON, &bsJSON, &m.WinningTeamID, &m.StartedAt, &m.EndedAt, &m.DeadlineAt, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(pzJSON, &m.PuzzleBoard)
	_ = json.Unmarshal(solJSON, &m.Solution)
	_ = json.Unmarshal(bsJSON, &m.BoardState)

	return &m, nil
}

func (r *Repository) GetMatchByID(ctx context.Context, matchID uuid.UUID) (*Match, error) {
	query := `
		SELECT m.id, m.game_mode_id, m.match_format_id, m.status, m.difficulty, m.puzzle_json, m.solution_json, m.board_state_json, m.winning_team_id, m.started_at, m.ended_at, m.deadline_at, m.created_at
		FROM matches m
		WHERE m.id = $1
	`
	var m Match
	var pzJSON, solJSON, bsJSON []byte
	err := r.db.Pool.QueryRow(ctx, query, matchID).Scan(&m.ID, &m.GameModeID, &m.MatchFormatID, &m.Status, &m.Difficulty, &pzJSON, &solJSON, &bsJSON, &m.WinningTeamID, &m.StartedAt, &m.EndedAt, &m.DeadlineAt, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(pzJSON, &m.PuzzleBoard)
	_ = json.Unmarshal(solJSON, &m.Solution)
	_ = json.Unmarshal(bsJSON, &m.BoardState)

	// Fetch Teams & Participants
	teamsQuery := `SELECT id, match_id, team_number, is_bot_team, bot_difficulty, score FROM match_teams WHERE match_id = $1 ORDER BY team_number`
	tRows, err := r.db.Pool.Query(ctx, teamsQuery, matchID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch teams: %w", err)
	}
	defer tRows.Close()
	for tRows.Next() {
		var t MatchTeam
		if err := tRows.Scan(&t.ID, &t.MatchID, &t.TeamNumber, &t.IsBotTeam, &t.BotDifficulty, &t.Score); err == nil {
			m.Teams = append(m.Teams, &t)
		}
	}
	if err := tRows.Err(); err != nil {
		return nil, fmt.Errorf("failed iterating teams: %w", err)
	}

	for _, t := range m.Teams {
		pQuery := `SELECT id, match_id, team_id, player_id, is_bot, slot_no, score, solved_cells, solved_time_seconds, result FROM match_participants WHERE team_id = $1 ORDER BY slot_no`
		pRows, err := r.db.Pool.Query(ctx, pQuery, t.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch participants: %w", err)
		}
		for pRows.Next() {
			var p MatchParticipant
			if err := pRows.Scan(&p.ID, &p.MatchID, &p.TeamID, &p.PlayerID, &p.IsBot, &p.SlotNo, &p.Score, &p.SolvedCells, &p.SolvedTimeSeconds, &p.Result); err == nil {
				t.Participants = append(t.Participants, &p)
			}
		}
		if err := pRows.Err(); err != nil {
			pRows.Close()
			return nil, fmt.Errorf("failed iterating participants: %w", err)
		}
		pRows.Close()
	}

	return &m, nil
}

// IsPlayerInMatch returns true if the player is a participant of the match.
// Membership check happens in SQL — no full match hydration needed.
func (r *Repository) IsPlayerInMatch(ctx context.Context, matchID uuid.UUID, playerID uuid.UUID) (bool, error) {
	var exists bool
	err := r.db.Pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM match_participants WHERE match_id = $1 AND player_id = $2)`,
		matchID, playerID,
	).Scan(&exists)
	return exists, err
}

// GetPlayerMatchIDs returns all match IDs the player participates in that are
// not finished/cancelled. Used to release stale active-match locks.
func (r *Repository) GetPlayerMatchIDs(ctx context.Context, playerID uuid.UUID, statuses ...Status) ([]uuid.UUID, error) {
	q := `SELECT DISTINCT match_id FROM match_participants WHERE player_id = $1`
	args := []interface{}{playerID}
	if len(statuses) > 0 {
		q += ` AND match_id IN (SELECT id FROM matches WHERE status = ANY($2))`
		ss := make([]string, len(statuses))
		for i, s := range statuses {
			ss[i] = string(s)
		}
		args = append(args, ss)
	}
	rows, err := r.db.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

func (r *Repository) UpdateMatchStatus(ctx context.Context, matchID uuid.UUID, status Status) error {
	var query string
	if status == StatusPlaying {
		query = `UPDATE matches SET status = $1, started_at = CURRENT_TIMESTAMP WHERE id = $2`
	} else if status == StatusFinished || status == StatusCancelled {
		query = `UPDATE matches SET status = $1, ended_at = CURRENT_TIMESTAMP WHERE id = $2`
	} else {
		query = `UPDATE matches SET status = $1 WHERE id = $2`
	}
	_, err := r.db.Pool.Exec(ctx, query, status, matchID)
	return err
}

// SaveMatchProgress persists live board state mid-match so a game-server
// restart does not wipe players' solved cells. Called periodically.
func (r *Repository) SaveMatchProgress(ctx context.Context, matchID uuid.UUID, boardState [81]int) error {
	bsJSON, err := json.Marshal(boardState)
	if err != nil {
		return fmt.Errorf("failed to marshal board state: %w", err)
	}
	_, err = r.db.Pool.Exec(ctx,
		`UPDATE matches SET board_state_json = $1 WHERE id = $2 AND status = 'playing'`,
		bsJSON, matchID,
	)
	if err != nil {
		return fmt.Errorf("failed to save match progress: %w", err)
	}
	return nil
}

// SaveMatchResult atomically finalizes a match: status, board, winner, scores,
// results AND player stats all in one transaction. Stats updates used to run on
// the pool outside the result transaction, silently diverging on failure.
func (r *Repository) SaveMatchResult(ctx context.Context, m *Match) error {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	bsJSON, err := json.Marshal(m.BoardState)
	if err != nil {
		return fmt.Errorf("failed to marshal board state: %w", err)
	}

	tag, err := tx.Exec(ctx,
		`UPDATE matches SET status = 'finished', board_state_json = $1, winning_team_id = $2, ended_at = CURRENT_TIMESTAMP
		 WHERE id = $3 AND status IN ('waiting', 'starting', 'playing')`,
		bsJSON, m.WinningTeamID, m.ID,
	)
	if err != nil {
		return fmt.Errorf("failed to finalize match: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Already finalized (e.g. by the deadline janitor) — nothing to do.
		return nil
	}

	for _, t := range m.Teams {
		_, err = tx.Exec(ctx, `UPDATE match_teams SET score = $1 WHERE id = $2`, t.Score, t.ID)
		if err != nil {
			return fmt.Errorf("failed to update team score: %w", err)
		}

		for _, p := range t.Participants {
			_, err = tx.Exec(ctx,
				`UPDATE match_participants SET score = $1, solved_cells = $2, result = $3 WHERE id = $4`,
				p.Score, p.SolvedCells, p.Result, p.ID,
			)
			if err != nil {
				return fmt.Errorf("failed to update participant result: %w", err)
			}

			if p.PlayerID != nil {
				isWin := p.Result != nil && *p.Result == "win"
				isLoss := p.Result != nil && *p.Result == "loss"
				err = stats.UpdatePlayerStatsTx(ctx, tx, *p.PlayerID, m.GameModeID, m.MatchFormatID, isWin, isLoss, p.Score, p.SolvedCells)
				if err != nil {
					return fmt.Errorf("failed to update player stats: %w", err)
				}
			}
		}
	}

	return tx.Commit(ctx)
}

// FinishExpiredMatches cancels matches whose deadline passed but that are still
// not finished. Returns the number of affected matches. Guarded on status so it
// never races with a concurrent SaveMatchResult.
func (r *Repository) FinishExpiredMatches(ctx context.Context, before time.Time) ([]uuid.UUID, error) {
	rows, err := r.db.Pool.Query(ctx,
		`UPDATE matches SET status = 'cancelled', ended_at = CURRENT_TIMESTAMP
		 WHERE status IN ('waiting', 'starting', 'playing') AND deadline_at IS NOT NULL AND deadline_at < $1
		 RETURNING id`,
		before,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}
