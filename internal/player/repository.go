package player

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/h3x02/suduxapi/internal/postgres"
)

type Player struct {
	ID         uuid.UUID `json:"id"`
	Email      string    `json:"email"`
	Name       *string   `json:"name"`
	IsActive   bool      `json:"is_active"`
	LastSeenAt time.Time `json:"last_seen_at"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Repository struct {
	db *postgres.DB
}

func NewRepository(db *postgres.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) GetByEmail(ctx context.Context, email string) (*Player, error) {
	query := `SELECT id, email, name, is_active, last_seen_at, created_at, updated_at FROM players WHERE email = $1`
	var p Player
	err := r.db.Pool.QueryRow(ctx, query, email).Scan(&p.ID, &p.Email, &p.Name, &p.IsActive, &p.LastSeenAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *Repository) GetByID(ctx context.Context, id uuid.UUID) (*Player, error) {
	query := `SELECT id, email, name, is_active, last_seen_at, created_at, updated_at FROM players WHERE id = $1`
	var p Player
	err := r.db.Pool.QueryRow(ctx, query, id).Scan(&p.ID, &p.Email, &p.Name, &p.IsActive, &p.LastSeenAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *Repository) Create(ctx context.Context, email string) (*Player, error) {
	query := `INSERT INTO players (email) VALUES ($1) RETURNING id, email, name, is_active, last_seen_at, created_at, updated_at`
	var p Player
	err := r.db.Pool.QueryRow(ctx, query, email).Scan(&p.ID, &p.Email, &p.Name, &p.IsActive, &p.LastSeenAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create player: %w", err)
	}
	return &p, nil
}

func (r *Repository) UpdateName(ctx context.Context, id uuid.UUID, name string) (*Player, error) {
	query := `UPDATE players SET name = $1, updated_at = CURRENT_TIMESTAMP WHERE id = $2 RETURNING id, email, name, is_active, last_seen_at, created_at, updated_at`
	var p Player
	err := r.db.Pool.QueryRow(ctx, query, name, id).Scan(&p.ID, &p.Email, &p.Name, &p.IsActive, &p.LastSeenAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to update player name: %w", err)
	}
	return &p, nil
}

func (r *Repository) SaveRefreshToken(ctx context.Context, playerID uuid.UUID, tokenHash, jti string, expiresAt time.Time) error {
	query := `INSERT INTO refresh_tokens (player_id, token_hash, jti, expires_at) VALUES ($1, $2, $3, $4)`
	_, err := r.db.Pool.Exec(ctx, query, playerID, tokenHash, jti, expiresAt)
	return err
}

// RotateRefreshToken atomically revokes the token identified by jti+hash only
// if it is still valid (not revoked, not expired). It returns true when the
// calling request won the rotation; false means unknown, already-rotated
// (reuse), or expired token. This closes the validate-then-revoke race where
// two concurrent refreshes could both succeed.
func (r *Repository) RotateRefreshToken(ctx context.Context, jti, tokenHash string) (bool, error) {
	tag, err := r.db.Pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked = TRUE
		 WHERE jti = $1 AND token_hash = $2 AND revoked = FALSE AND expires_at > CURRENT_TIMESTAMP`,
		jti, tokenHash,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// RevokeAllPlayerRefreshTokens is the kill-switch for detected token reuse.
func (r *Repository) RevokeAllPlayerRefreshTokens(ctx context.Context, playerID uuid.UUID) error {
	_, err := r.db.Pool.Exec(ctx, `UPDATE refresh_tokens SET revoked = TRUE WHERE player_id = $1`, playerID)
	return err
}

func (r *Repository) RevokeRefreshTokenByJTI(ctx context.Context, jti string) error {
	query := `UPDATE refresh_tokens SET revoked = TRUE WHERE jti = $1`
	_, err := r.db.Pool.Exec(ctx, query, jti)
	return err
}

func (r *Repository) IsRefreshTokenValid(ctx context.Context, jti string, tokenHash string) (bool, error) {
	query := `SELECT count(*) FROM refresh_tokens WHERE jti = $1 AND token_hash = $2 AND revoked = FALSE AND expires_at > CURRENT_TIMESTAMP`
	var count int
	err := r.db.Pool.QueryRow(ctx, query, jti, tokenHash).Scan(&count)
	return count > 0, err
}
