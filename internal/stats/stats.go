package stats

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/h3x02/suduxapi/internal/middleware"
	"github.com/h3x02/suduxapi/internal/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// execer is satisfied by both pgx.Tx and *pgxpool.Pool.
type execer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

type PlayerStats struct {
	ID               uuid.UUID `json:"id"`
	PlayerID         uuid.UUID `json:"player_id"`
	GameModeID       int       `json:"game_mode_id"`
	MatchFormatID    int       `json:"match_format_id"`
	MatchesPlayed    int       `json:"matches_played"`
	Wins             int       `json:"wins"`
	Losses           int       `json:"losses"`
	Draws            int       `json:"draws"`
	BestScore        int       `json:"best_score"`
	BestTimeSeconds  *int      `json:"best_time_seconds,omitempty"`
	TotalSolvedCells int       `json:"total_solved_cells"`
}

type Repository struct {
	db *postgres.DB
}

func NewRepository(db *postgres.DB) *Repository {
	return &Repository{db: db}
}

// UpdatePlayerStatsTx runs the stats upsert inside the caller's transaction so
// match results and stats commit atomically. Both *pgxpool.Pool and pgx.Tx
// satisfy the pgx.Tx interface for Exec purposes.
func UpdatePlayerStatsTx(ctx context.Context, tx pgx.Tx, playerID uuid.UUID, gameModeID, matchFormatID int, isWin, isLoss bool, score, solvedCells int) error {
	return upsertPlayerStats(ctx, tx, playerID, gameModeID, matchFormatID, isWin, isLoss, score, solvedCells)
}

func (r *Repository) UpdatePlayerStats(ctx context.Context, playerID uuid.UUID, gameModeID, matchFormatID int, isWin, isLoss bool, score, solvedCells int) error {
	return upsertPlayerStats(ctx, r.db.Pool, playerID, gameModeID, matchFormatID, isWin, isLoss, score, solvedCells)
}

func upsertPlayerStats(ctx context.Context, db execer, playerID uuid.UUID, gameModeID, matchFormatID int, isWin, isLoss bool, score, solvedCells int) error {
	winInc := 0
	lossInc := 0
	drawInc := 0
	if isWin {
		winInc = 1
	} else if isLoss {
		lossInc = 1
	} else {
		drawInc = 1
	}

	query := `
		INSERT INTO stats (player_id, game_mode_id, match_format_id, matches_played, wins, losses, draws, best_score, total_solved_cells)
		VALUES ($1, $2, $3, 1, $4, $5, $6, $7, $8)
		ON CONFLICT (player_id, game_mode_id, match_format_id)
		DO UPDATE SET
			matches_played = stats.matches_played + 1,
			wins = stats.wins + EXCLUDED.wins,
			losses = stats.losses + EXCLUDED.losses,
			draws = stats.draws + EXCLUDED.draws,
			best_score = GREATEST(stats.best_score, EXCLUDED.best_score),
			total_solved_cells = stats.total_solved_cells + EXCLUDED.total_solved_cells,
			updated_at = CURRENT_TIMESTAMP
	`
	_, err := db.Exec(ctx, query, playerID, gameModeID, matchFormatID, winInc, lossInc, drawInc, score, solvedCells)
	return err
}

func (r *Repository) GetPlayerStats(ctx context.Context, playerID uuid.UUID) ([]*PlayerStats, error) {
	query := `SELECT id, player_id, game_mode_id, match_format_id, matches_played, wins, losses, draws, best_score, best_time_seconds, total_solved_cells FROM stats WHERE player_id = $1`
	rows, err := r.db.Pool.Query(ctx, query, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*PlayerStats
	for rows.Next() {
		var s PlayerStats
		if err := rows.Scan(&s.ID, &s.PlayerID, &s.GameModeID, &s.MatchFormatID, &s.MatchesPlayed, &s.Wins, &s.Losses, &s.Draws, &s.BestScore, &s.BestTimeSeconds, &s.TotalSolvedCells); err == nil {
			list = append(list, &s)
		}
	}
	return list, rows.Err()
}

type Handler struct {
	repo *Repository
}

func NewHandler(repo *Repository) *Handler {
	return &Handler{repo: repo}
}

func (h *Handler) GetMyStats(w http.ResponseWriter, r *http.Request) {
	playerIDStr, ok := middleware.GetPlayerID(r.Context())
	if !ok {
		respondError(w, http.StatusUnauthorized, "UNAUTHORIZED", "User not authenticated")
		return
	}
	userID, _ := uuid.Parse(playerIDStr)

	list, err := h.repo.GetPlayerStats(r.Context(), userID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "DB_ERROR", "Failed to fetch player stats")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(list)
}

func respondError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}
