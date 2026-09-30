package friendship

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/h3x02/suduxapi/internal/postgres"
)

type Status string

const (
	StatusPending  Status = "pending"
	StatusAccepted Status = "accepted"
	StatusRejected Status = "rejected"
	StatusBlocked  Status = "blocked"
)

type Friendship struct {
	ID          uuid.UUID `json:"id"`
	RequesterID uuid.UUID `json:"requester_id"`
	AddresseeID uuid.UUID `json:"addressee_id"`
	Status      Status    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type FriendInfo struct {
	FriendshipID uuid.UUID  `json:"friendship_id"`
	PlayerID     uuid.UUID  `json:"player_id"`
	Email        string     `json:"email"`
	Name         *string    `json:"name"`
	Status       Status     `json:"status"`
	LastSeenAt   time.Time  `json:"last_seen_at"`
}

type Repository struct {
	db *postgres.DB
}

func NewRepository(db *postgres.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) SendRequest(ctx context.Context, requesterID, addresseeID uuid.UUID) (*Friendship, error) {
	if requesterID == addresseeID {
		return nil, errors.New("cannot add yourself as friend")
	}

	query := `
		INSERT INTO friendships (requester_id, addressee_id, status)
		VALUES ($1, $2, 'pending')
		ON CONFLICT ((LEAST(requester_id, addressee_id)), (GREATEST(requester_id, addressee_id)))
		DO UPDATE SET requester_id = EXCLUDED.requester_id, addressee_id = EXCLUDED.addressee_id, status = 'pending', updated_at = CURRENT_TIMESTAMP
		WHERE friendships.status IN ('rejected')
		RETURNING id, requester_id, addressee_id, status, created_at, updated_at
	`
	var f Friendship
	err := r.db.Pool.QueryRow(ctx, query, requesterID, addresseeID).Scan(&f.ID, &f.RequesterID, &f.AddresseeID, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to send friend request or relationship already exists: %w", err)
	}
	return &f, nil
}

func (r *Repository) AcceptRequest(ctx context.Context, friendshipID, userID uuid.UUID) (*Friendship, error) {
	query := `
		UPDATE friendships
		SET status = 'accepted', updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND addressee_id = $2 AND status = 'pending'
		RETURNING id, requester_id, addressee_id, status, created_at, updated_at
	`
	var f Friendship
	err := r.db.Pool.QueryRow(ctx, query, friendshipID, userID).Scan(&f.ID, &f.RequesterID, &f.AddresseeID, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to accept request or not authorized: %w", err)
	}
	return &f, nil
}

func (r *Repository) RejectRequest(ctx context.Context, friendshipID, userID uuid.UUID) error {
	query := `
		UPDATE friendships
		SET status = 'rejected', updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND addressee_id = $2 AND status = 'pending'
	`
	res, err := r.db.Pool.Exec(ctx, query, friendshipID, userID)
	if err != nil || res.RowsAffected() == 0 {
		return errors.New("friend request not found or not authorized")
	}
	return nil
}

func (r *Repository) RemoveFriend(ctx context.Context, friendID, userID uuid.UUID) error {
	query := `
		DELETE FROM friendships
		WHERE (LEAST(requester_id, addressee_id) = LEAST($1::uuid, $2::uuid))
		  AND (GREATEST(requester_id, addressee_id) = GREATEST($1::uuid, $2::uuid))
		  AND status = 'accepted'
	`
	res, err := r.db.Pool.Exec(ctx, query, friendID, userID)
	if err != nil || res.RowsAffected() == 0 {
		return errors.New("friendship not found")
	}
	return nil
}

func (r *Repository) BlockPlayer(ctx context.Context, targetID, userID uuid.UUID) error {
	query := `
		INSERT INTO friendships (requester_id, addressee_id, status)
		VALUES ($1, $2, 'blocked')
		ON CONFLICT ((LEAST(requester_id, addressee_id)), (GREATEST(requester_id, addressee_id)))
		DO UPDATE SET requester_id = EXCLUDED.requester_id, addressee_id = EXCLUDED.addressee_id, status = 'blocked', updated_at = CURRENT_TIMESTAMP
	`
	_, err := r.db.Pool.Exec(ctx, query, userID, targetID)
	return err
}

func (r *Repository) GetFriends(ctx context.Context, userID uuid.UUID) ([]*FriendInfo, error) {
	query := `
		SELECT f.id, p.id, p.email, p.name, f.status, p.last_seen_at
		FROM friendships f
		JOIN players p ON (CASE WHEN f.requester_id = $1 THEN f.addressee_id ELSE f.requester_id END) = p.id
		WHERE (f.requester_id = $1 OR f.addressee_id = $1)
		  AND f.status = 'accepted'
	`
	rows, err := r.db.Pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []*FriendInfo
	for rows.Next() {
		var info FriendInfo
		if err := rows.Scan(&info.FriendshipID, &info.PlayerID, &info.Email, &info.Name, &info.Status, &info.LastSeenAt); err != nil {
			return nil, err
		}
		list = append(list, &info)
	}
	return list, nil
}
