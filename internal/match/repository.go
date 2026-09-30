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
	ID             uuid.UUID         `json:"id"`
	GameModeID     int               `json:"game_mode_id"`
	MatchFormatID  int               `json:"match_format_id"`
	Status         Status            `json:"status"`
	Difficulty     puzzle.Difficulty `json:"difficulty"`
	PuzzleBoard    [81]int           `json:"puzzle_board"`
	Solution       [81]int           `json:"solution"`
	BoardState     [81]int           `json:"board_state"`
	CellOwners     [81]string        `json:"cell_owners"` // player_id or bot_id string
	WinningTeamID  *uuid.UUID        `json:"winning_team_id,omitempty"`
	StartedAt      *time.Time        `json:"started_at,omitempty"`
	EndedAt        *time.Time        `json:"ended_at,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	Teams          []*MatchTeam      `json:"teams"`
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

func (r *Repository) CreateMatch(ctx context.Context, gameModeKey, matchFormatKey string, diff puzzle.Difficulty, pz *puzzle.Puzzle) (*Match, error) {
	var gameModeID, matchFormatID int
	err := r.db.Pool.QueryRow(ctx, `SELECT id FROM game_modes WHERE key = $1`, gameModeKey).Scan(&gameModeID)
	if err != nil {
		return nil, fmt.Errorf("invalid game mode: %w", err)
	}
	err = r.db.Pool.QueryRow(ctx, `SELECT id FROM match_formats WHERE key = $1`, matchFormatKey).Scan(&matchFormatID)
	if err != nil {
		return nil, fmt.Errorf("invalid match format: %w", err)
	}

	puzzleJSON, _ := json.Marshal(pz.Board)
	solutionJSON, _ := json.Marshal(pz.Solution)
	boardStateJSON, _ := json.Marshal(pz.Board)

	query := `
		INSERT INTO matches (game_mode_id, match_format_id, status, difficulty, puzzle_json, solution_json, board_state_json)
		VALUES ($1, $2, 'waiting', $3, $4, $5, $6)
		RETURNING id, created_at
	`
	m := &Match{
		GameModeID:    gameModeID,
		MatchFormatID: matchFormatID,
		Status:        StatusWaiting,
		Difficulty:    diff,
		PuzzleBoard:   pz.Board,
		Solution:      pz.Solution,
		BoardState:    pz.Board,
	}

	err = r.db.Pool.QueryRow(ctx, query, gameModeID, matchFormatID, diff, puzzleJSON, solutionJSON, boardStateJSON).Scan(&m.ID, &m.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to insert match: %w", err)
	}

	return m, nil
}

func (r *Repository) AddTeam(ctx context.Context, matchID uuid.UUID, teamNumber int, isBotTeam bool, botDiff *bot.Difficulty) (*MatchTeam, error) {
	query := `
		INSERT INTO match_teams (match_id, team_number, is_bot_team, bot_difficulty)
		VALUES ($1, $2, $3, $4)
		RETURNING id, match_id, team_number, is_bot_team, bot_difficulty, score
	`
	t := &MatchTeam{}
	err := r.db.Pool.QueryRow(ctx, query, matchID, teamNumber, isBotTeam, botDiff).Scan(&t.ID, &t.MatchID, &t.TeamNumber, &t.IsBotTeam, &t.BotDifficulty, &t.Score)
	if err != nil {
		return nil, fmt.Errorf("failed to add team: %w", err)
	}
	return t, nil
}

func (r *Repository) AddParticipant(ctx context.Context, matchID, teamID uuid.UUID, playerID *uuid.UUID, isBot bool, slotNo int) (*MatchParticipant, error) {
	query := `
		INSERT INTO match_participants (match_id, team_id, player_id, is_bot, slot_no)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, match_id, team_id, player_id, is_bot, slot_no, score, solved_cells
	`
	p := &MatchParticipant{}
	err := r.db.Pool.QueryRow(ctx, query, matchID, teamID, playerID, isBot, slotNo).Scan(&p.ID, &p.MatchID, &p.TeamID, &p.PlayerID, &p.IsBot, &p.SlotNo, &p.Score, &p.SolvedCells)
	if err != nil {
		return nil, fmt.Errorf("failed to add participant: %w", err)
	}
	return p, nil
}

func (r *Repository) GetActiveMatchByPlayer(ctx context.Context, playerID uuid.UUID) (*Match, error) {
	query := `
		SELECT m.id, m.game_mode_id, m.match_format_id, m.status, m.difficulty, m.puzzle_json, m.solution_json, m.board_state_json, m.winning_team_id, m.started_at, m.ended_at, m.created_at
		FROM matches m
		JOIN match_participants mp ON m.id = mp.match_id
		WHERE mp.player_id = $1 AND m.status IN ('waiting', 'starting', 'playing')
		LIMIT 1
	`
	var m Match
	var pzJSON, solJSON, bsJSON []byte
	err := r.db.Pool.QueryRow(ctx, query, playerID).Scan(&m.ID, &m.GameModeID, &m.MatchFormatID, &m.Status, &m.Difficulty, &pzJSON, &solJSON, &bsJSON, &m.WinningTeamID, &m.StartedAt, &m.EndedAt, &m.CreatedAt)
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
		SELECT m.id, m.game_mode_id, m.match_format_id, m.status, m.difficulty, m.puzzle_json, m.solution_json, m.board_state_json, m.winning_team_id, m.started_at, m.ended_at, m.created_at
		FROM matches m
		WHERE m.id = $1
	`
	var m Match
	var pzJSON, solJSON, bsJSON []byte
	err := r.db.Pool.QueryRow(ctx, query, matchID).Scan(&m.ID, &m.GameModeID, &m.MatchFormatID, &m.Status, &m.Difficulty, &pzJSON, &solJSON, &bsJSON, &m.WinningTeamID, &m.StartedAt, &m.EndedAt, &m.CreatedAt)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(pzJSON, &m.PuzzleBoard)
	_ = json.Unmarshal(solJSON, &m.Solution)
	_ = json.Unmarshal(bsJSON, &m.BoardState)

	// Fetch Teams & Participants
	teamsQuery := `SELECT id, match_id, team_number, is_bot_team, bot_difficulty, score FROM match_teams WHERE match_id = $1 ORDER BY team_number`
	tRows, err := r.db.Pool.Query(ctx, teamsQuery, matchID)
	if err == nil {
		defer tRows.Close()
		for tRows.Next() {
			var t MatchTeam
			if err := tRows.Scan(&t.ID, &t.MatchID, &t.TeamNumber, &t.IsBotTeam, &t.BotDifficulty, &t.Score); err == nil {
				m.Teams = append(m.Teams, &t)
			}
		}
	}

	for _, t := range m.Teams {
		pQuery := `SELECT id, match_id, team_id, player_id, is_bot, slot_no, score, solved_cells, solved_time_seconds, result FROM match_participants WHERE team_id = $1 ORDER BY slot_no`
		pRows, err := r.db.Pool.Query(ctx, pQuery, t.ID)
		if err == nil {
			defer pRows.Close()
			for pRows.Next() {
				var p MatchParticipant
				if err := pRows.Scan(&p.ID, &p.MatchID, &p.TeamID, &p.PlayerID, &p.IsBot, &p.SlotNo, &p.Score, &p.SolvedCells, &p.SolvedTimeSeconds, &p.Result); err == nil {
					t.Participants = append(t.Participants, &p)
				}
			}
		}
	}

	return &m, nil
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

func (r *Repository) SaveMatchResult(ctx context.Context, m *Match) error {
	tx, err := r.db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	bsJSON, _ := json.Marshal(m.BoardState)
	_, err = tx.Exec(ctx, `UPDATE matches SET status = 'finished', board_state_json = $1, winning_team_id = $2, ended_at = CURRENT_TIMESTAMP WHERE id = $3`, bsJSON, m.WinningTeamID, m.ID)
	if err != nil {
		return err
	}

	for _, t := range m.Teams {
		_, err = tx.Exec(ctx, `UPDATE match_teams SET score = $1 WHERE id = $2`, t.Score, t.ID)
		if err != nil {
			return err
		}

		for _, p := range t.Participants {
			_, err = tx.Exec(ctx, `UPDATE match_participants SET score = $1, solved_cells = $2, result = $3 WHERE id = $4`, p.Score, p.SolvedCells, p.Result, p.ID)
			if err != nil {
				return err
			}

			// Update stats for real players
			if p.PlayerID != nil {
				isWin := p.Result != nil && *p.Result == "win"
				isLoss := p.Result != nil && *p.Result == "loss"
				_ = r.statsRepo.UpdatePlayerStats(ctx, *p.PlayerID, m.GameModeID, m.MatchFormatID, isWin, isLoss, p.Score, p.SolvedCells)
			}
		}
	}

	return tx.Commit(ctx)
}
