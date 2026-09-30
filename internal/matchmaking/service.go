package matchmaking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/h3x02/suduxapi/internal/bot"
	"github.com/h3x02/suduxapi/internal/match"
	"github.com/h3x02/suduxapi/internal/puzzle"
	"github.com/h3x02/suduxapi/internal/redis"
)

var (
	ErrPlayerAlreadyInMatch = errors.New("player already has an active match")
	ErrPlayerAlreadyInQueue = errors.New("player is already in matchmaking queue")
)

type JoinRequest struct {
	PlayerID      string            `json:"player_id"`
	GameMode      string            `json:"game_mode"`      // "cell_race"
	MatchFormat   string            `json:"match_format"`   // "1v1", "1_team_vs_bot"
	Difficulty    puzzle.Difficulty `json:"difficulty"`     // "easy", "medium", "hard", "expert"
	BotDifficulty *bot.Difficulty   `json:"bot_difficulty"` // optional for 1_team_vs_bot
	WithTeammate  bool              `json:"with_teammate"`  // if false, solo team against bot
	FriendID      *string           `json:"friend_id"`      // optional friend invite ID
}

type MatchCreatedEvent struct {
	MatchID     string   `json:"match_id"`
	PlayerIDs   []string `json:"player_ids"`
	GameMode    string   `json:"game_mode"`
	MatchFormat string   `json:"match_format"`
}

type Service struct {
	redis     *redis.Client
	matchRepo *match.Repository
	generator *puzzle.Generator
}

func NewService(rdb *redis.Client, matchRepo *match.Repository) *Service {
	return &Service{
		redis:     rdb,
		matchRepo: matchRepo,
		generator: puzzle.NewGenerator(),
	}
}

func (s *Service) Create1v1MatchExternal(ctx context.Context, p1ID, p2ID, mode string, diff puzzle.Difficulty) (*MatchCreatedEvent, error) {
	return s.create1v1Match(ctx, p1ID, p2ID, mode, diff)
}

func (s *Service) CreateBotMatchExternal(ctx context.Context, p1ID string, p2ID *string, mode string, diff puzzle.Difficulty, botDiff bot.Difficulty) (*MatchCreatedEvent, error) {
	return s.createBotMatch(ctx, p1ID, p2ID, mode, diff, botDiff)
}

// LockActiveMatch acquires an active match lock for a player in Redis
func (s *Service) LockActiveMatch(ctx context.Context, playerID string, matchID string) (bool, error) {
	key := fmt.Sprintf("active_match:%s", playerID)
	// Active match lock expires after 1 hour max or when match ends
	ok, err := s.redis.SetNX(ctx, key, matchID, 1*time.Hour).Result()
	return ok, err
}

// UnlockActiveMatch removes active match lock for player
func (s *Service) UnlockActiveMatch(ctx context.Context, playerID string) error {
	key := fmt.Sprintf("active_match:%s", playerID)
	return s.redis.Del(ctx, key).Err()
}

// HasActiveMatch checks if player is currently in an active match
func (s *Service) HasActiveMatch(ctx context.Context, playerID string) (bool, string, error) {
	key := fmt.Sprintf("active_match:%s", playerID)
	matchID, err := s.redis.Get(ctx, key).Result()
	if err == nil && matchID != "" {
		return true, matchID, nil
	}
	return false, "", nil
}

func (s *Service) JoinQueue(ctx context.Context, req *JoinRequest) (*MatchCreatedEvent, error) {
	// 1. Check if player has active match
	hasMatch, activeMatchID, err := s.HasActiveMatch(ctx, req.PlayerID)
	if err == nil && hasMatch {
		return nil, fmt.Errorf("%w: match_id %s", ErrPlayerAlreadyInMatch, activeMatchID)
	}

	// 2. Instant solo-bot match creation
	if req.MatchFormat == "1_team_vs_bot" && !req.WithTeammate && req.FriendID == nil {
		botDiff := bot.DifficultyNoob
		if req.BotDifficulty != nil {
			botDiff = *req.BotDifficulty
		}
		return s.createBotMatch(ctx, req.PlayerID, nil, req.GameMode, req.Difficulty, botDiff)
	}

	// 3. Friend invite match creation
	if req.FriendID != nil && *req.FriendID != "" {
		friendID := *req.FriendID
		hasMatchFriend, _, _ := s.HasActiveMatch(ctx, friendID)
		if hasMatchFriend {
			return nil, errors.New("friend is currently in another match")
		}

		if req.MatchFormat == "1_team_vs_bot" {
			botDiff := bot.DifficultyNoob
			if req.BotDifficulty != nil {
				botDiff = *req.BotDifficulty
			}
			return s.createBotMatch(ctx, req.PlayerID, &friendID, req.GameMode, req.Difficulty, botDiff)
		} else if req.MatchFormat == "1v1" {
			return s.create1v1Match(ctx, req.PlayerID, friendID, req.GameMode, req.Difficulty)
		}
	}

	// 4. Standard matchmaking queue in Redis
	queueKey := fmt.Sprintf("matchmaking:queue:%s:%s:%s", req.GameMode, req.MatchFormat, req.Difficulty)
	playerQueueKey := fmt.Sprintf("matchmaking:player:%s", req.PlayerID)

	// Set player queue status
	reqBytes, _ := json.Marshal(req)
	added, err := s.redis.SetNX(ctx, playerQueueKey, string(reqBytes), 5*time.Minute).Result()
	if err != nil || !added {
		return nil, ErrPlayerAlreadyInQueue
	}

	// Add player to matchmaking queue
	s.redis.RPush(ctx, queueKey, req.PlayerID)

	return nil, nil // Waiting in queue
}

func (s *Service) LeaveQueue(ctx context.Context, playerID string) error {
	playerQueueKey := fmt.Sprintf("matchmaking:player:%s", playerID)
	val, err := s.redis.Get(ctx, playerQueueKey).Result()
	if err != nil {
		return nil // Not in queue
	}

	var req JoinRequest
	_ = json.Unmarshal([]byte(val), &req)

	queueKey := fmt.Sprintf("matchmaking:queue:%s:%s:%s", req.GameMode, req.MatchFormat, req.Difficulty)
	s.redis.LRem(ctx, queueKey, 0, playerID)
	s.redis.Del(ctx, playerQueueKey)

	return nil
}

func (s *Service) create1v1Match(ctx context.Context, player1ID, player2ID, gameMode string, diff puzzle.Difficulty) (*MatchCreatedEvent, error) {
	pz, err := s.generator.Generate(diff)
	if err != nil {
		return nil, fmt.Errorf("failed to generate puzzle: %w", err)
	}

	m, err := s.matchRepo.CreateMatch(ctx, gameMode, "1v1", diff, pz)
	if err != nil {
		return nil, err
	}

	p1UUID, _ := uuid.Parse(player1ID)
	p2UUID, _ := uuid.Parse(player2ID)

	// Team 1
	t1, err := s.matchRepo.AddTeam(ctx, m.ID, 1, false, nil)
	if err != nil {
		return nil, err
	}
	_, _ = s.matchRepo.AddParticipant(ctx, m.ID, t1.ID, &p1UUID, false, 1)

	// Team 2
	t2, err := s.matchRepo.AddTeam(ctx, m.ID, 2, false, nil)
	if err != nil {
		return nil, err
	}
	_, _ = s.matchRepo.AddParticipant(ctx, m.ID, t2.ID, &p2UUID, false, 1)

	// Update status to playing
	_ = s.matchRepo.UpdateMatchStatus(ctx, m.ID, match.StatusPlaying)

	// Set active match locks
	s.LockActiveMatch(ctx, player1ID, m.ID.String())
	s.LockActiveMatch(ctx, player2ID, m.ID.String())

	event := &MatchCreatedEvent{
		MatchID:     m.ID.String(),
		PlayerIDs:   []string{player1ID, player2ID},
		GameMode:    gameMode,
		MatchFormat: "1v1",
	}

	// Publish signaling event over Redis Pub/Sub
	eventBytes, _ := json.Marshal(event)
	s.redis.Publish(ctx, "matchmaking:match_created", string(eventBytes))

	return event, nil
}

func (s *Service) createBotMatch(ctx context.Context, player1ID string, player2ID *string, gameMode string, diff puzzle.Difficulty, botDiff bot.Difficulty) (*MatchCreatedEvent, error) {
	pz, err := s.generator.Generate(diff)
	if err != nil {
		return nil, fmt.Errorf("failed to generate puzzle: %w", err)
	}

	m, err := s.matchRepo.CreateMatch(ctx, gameMode, "1_team_vs_bot", diff, pz)
	if err != nil {
		return nil, err
	}

	p1UUID, _ := uuid.Parse(player1ID)

	// Human Team 1
	t1, err := s.matchRepo.AddTeam(ctx, m.ID, 1, false, nil)
	if err != nil {
		return nil, err
	}
	_, _ = s.matchRepo.AddParticipant(ctx, m.ID, t1.ID, &p1UUID, false, 1)

	playersList := []string{player1ID}

	if player2ID != nil {
		p2UUID, _ := uuid.Parse(*player2ID)
		_, _ = s.matchRepo.AddParticipant(ctx, m.ID, t1.ID, &p2UUID, false, 2)
		playersList = append(playersList, *player2ID)
	}

	// Bot Team 2
	t2, err := s.matchRepo.AddTeam(ctx, m.ID, 2, true, &botDiff)
	if err != nil {
		return nil, err
	}
	_, _ = s.matchRepo.AddParticipant(ctx, m.ID, t2.ID, nil, true, 1)

	// Update status to playing
	_ = s.matchRepo.UpdateMatchStatus(ctx, m.ID, match.StatusPlaying)

	// Set active match locks
	for _, pID := range playersList {
		s.LockActiveMatch(ctx, pID, m.ID.String())
	}

	event := &MatchCreatedEvent{
		MatchID:     m.ID.String(),
		PlayerIDs:   playersList,
		GameMode:    gameMode,
		MatchFormat: "1_team_vs_bot",
	}

	// Publish signaling event over Redis Pub/Sub
	eventBytes, _ := json.Marshal(event)
	s.redis.Publish(ctx, "matchmaking:match_created", string(eventBytes))

	return event, nil
}
