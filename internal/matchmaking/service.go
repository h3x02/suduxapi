package matchmaking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/h3x02/suduxapi/internal/activematch"
	"github.com/h3x02/suduxapi/internal/bot"
	"github.com/h3x02/suduxapi/internal/config"
	"github.com/h3x02/suduxapi/internal/match"
	"github.com/h3x02/suduxapi/internal/puzzle"
	"github.com/h3x02/suduxapi/internal/redis"
)

var (
	ErrPlayerAlreadyInMatch = errors.New("player already has an active match")
	ErrPlayerAlreadyInQueue = errors.New("player is already in matchmaking queue")
)

var (
	ValidGameModes = []string{"cell_race"}
	ValidFormats   = []string{"1v1", "1_team_vs_bot"}
	ValidDifficulties = []puzzle.Difficulty{
		puzzle.DifficultyEasy, puzzle.DifficultyMedium, puzzle.DifficultyHard, puzzle.DifficultyExpert,
	}
	ValidBotDifficulties = []bot.Difficulty{
		bot.DifficultyNoob, bot.DifficultyPro, bot.DifficultyExpert, bot.DifficultyDevil,
	}
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

// Validate whitelists enum values. They are used to build Redis keys and DB
// lookups, so arbitrary client-controlled strings must be rejected.
func (req *JoinRequest) Validate() error {
	if !containsString(ValidGameModes, req.GameMode) {
		return fmt.Errorf("invalid game_mode: %s", req.GameMode)
	}
	if !containsString(ValidFormats, req.MatchFormat) {
		return fmt.Errorf("invalid match_format: %s", req.MatchFormat)
	}
	if !containsDifficulty(ValidDifficulties, req.Difficulty) {
		return fmt.Errorf("invalid difficulty: %s", req.Difficulty)
	}
	if req.BotDifficulty != nil && !containsBotDifficulty(ValidBotDifficulties, *req.BotDifficulty) {
		return fmt.Errorf("invalid bot_difficulty: %s", *req.BotDifficulty)
	}
	if req.MatchFormat != "1_team_vs_bot" && req.BotDifficulty != nil {
		return fmt.Errorf("bot_difficulty is only valid for 1_team_vs_bot")
	}
	if req.FriendID != nil && *req.FriendID != "" {
		if _, err := uuid.Parse(*req.FriendID); err != nil {
			return fmt.Errorf("invalid friend_id")
		}
		if *req.FriendID == req.PlayerID {
			return errors.New("cannot invite yourself")
		}
	}
	// Team matchmaking (finding an unknown teammate) is not implemented; the
	// matchmaker only pairs 1v1 players. Rejecting here beats letting the
	// request rot in a queue that is never drained.
	if req.MatchFormat == "1_team_vs_bot" && req.WithTeammate && (req.FriendID == nil || *req.FriendID == "") {
		return errors.New("team matchmaking without a friend_id is not supported yet")
	}
	return nil
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
	cfg       *config.Config
}

func NewService(rdb *redis.Client, matchRepo *match.Repository, cfg *config.Config) *Service {
	return &Service{
		redis:     rdb,
		matchRepo: matchRepo,
		generator: puzzle.NewGenerator(),
		cfg:       cfg,
	}
}

// Create1v1MatchExternal is used by the matchmaker worker to create a 1v1
// match between two queue-popped players.
func (s *Service) Create1v1MatchExternal(ctx context.Context, p1ID, p2ID, mode string, diff puzzle.Difficulty) (*MatchCreatedEvent, error) {
	return s.create1v1Match(ctx, p1ID, p2ID, mode, diff)
}

// lockTTL covers the maximum match duration plus buffer. It must outlive the
// server-side match deadline, otherwise a still-running match would lose its
// protection and let a player join a second match.
func (s *Service) lockTTL() time.Duration {
	return s.cfg.MatchMaxDuration + 10*time.Minute
}

// HasActiveMatch checks Redis first (fast path), then falls back to Postgres.
// The DB check is authoritative: Redis locks can expire while the match is
// still running, or survive as stale locks after a match finished on a
// different process that couldn't reach Redis.
func (s *Service) HasActiveMatch(ctx context.Context, playerID string) (bool, string, error) {
	// Fast path: Redis lock.
	matchID, err := activematch.Get(ctx, s.redis.Client, playerID)
	if err == nil && matchID != "" {
		mUUID, parseErr := uuid.Parse(matchID)
		if parseErr != nil {
			// Corrupted lock value — clean it up.
			_ = activematch.Unlock(ctx, s.redis.Client, playerID)
		} else {
			m, dbErr := s.matchRepo.GetMatchByID(ctx, mUUID)
			if dbErr == nil && isActiveStatus(m.Status) {
				return true, matchID, nil
			}
			// Stale lock pointing at a finished/cancelled/missing match.
			_ = activematch.Unlock(ctx, s.redis.Client, playerID)
		}
	}

	// Authoritative fallback: DB.
	pID, parseErr := uuid.Parse(playerID)
	if parseErr != nil {
		return false, "", nil
	}
	ids, err := s.matchRepo.GetPlayerMatchIDs(ctx, pID, match.StatusWaiting, match.StatusStarting, match.StatusPlaying)
	if err != nil {
		return false, "", err
	}
	if len(ids) == 0 {
		return false, "", nil
	}
	return true, ids[0].String(), nil
}

func isActiveStatus(s match.Status) bool {
	return s == match.StatusWaiting || s == match.StatusStarting || s == match.StatusPlaying
}

// ReleasePlayerLocks removes the active-match lock for a player. Called by the
// game server when a match finishes/cancels so the player can queue again.
func (s *Service) ReleasePlayerLocks(ctx context.Context, playerIDs []string) {
	for _, pID := range playerIDs {
		if err := activematch.Unlock(ctx, s.redis.Client, pID); err != nil {
			continue
		}
	}
}

func (s *Service) JoinQueue(ctx context.Context, req *JoinRequest) (*MatchCreatedEvent, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}

	// 1. Check if player has active match (Redis + DB fallback)
	hasMatch, activeMatchID, err := s.HasActiveMatch(ctx, req.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("failed to check active match: %w", err)
	}
	if hasMatch {
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
		hasMatchFriend, _, err := s.HasActiveMatch(ctx, friendID)
		if err != nil {
			return nil, fmt.Errorf("failed to check friend's active match: %w", err)
		}
		if hasMatchFriend {
			return nil, errors.New("friend is currently in another match")
		}

		if req.MatchFormat == "1_team_vs_bot" {
			botDiff := bot.DifficultyNoob
			if req.BotDifficulty != nil {
				botDiff = *req.BotDifficulty
			}
			return s.createBotMatch(ctx, req.PlayerID, &friendID, req.GameMode, req.Difficulty, botDiff)
		}
		return s.create1v1Match(ctx, req.PlayerID, friendID, req.GameMode, req.Difficulty)
	}

	// 4. Standard matchmaking queue in Redis
	queueKey := fmt.Sprintf("matchmaking:queue:%s:%s:%s", req.GameMode, req.MatchFormat, req.Difficulty)
	playerQueueKey := fmt.Sprintf("matchmaking:player:%s", req.PlayerID)

	// Set player queue status
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal join request: %w", err)
	}
	added, err := s.redis.SetNX(ctx, playerQueueKey, string(reqBytes), 5*time.Minute).Result()
	if err != nil {
		return nil, fmt.Errorf("redis error: %w", err)
	}
	if !added {
		return nil, ErrPlayerAlreadyInQueue
	}

	// Add player to matchmaking queue
	if err := s.redis.RPush(ctx, queueKey, req.PlayerID).Err(); err != nil {
		s.redis.Del(ctx, playerQueueKey)
		return nil, fmt.Errorf("failed to enqueue player: %w", err)
	}

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

	p1UUID, err := uuid.Parse(player1ID)
	if err != nil {
		return nil, fmt.Errorf("invalid player1 id: %w", err)
	}
	p2UUID, err := uuid.Parse(player2ID)
	if err != nil {
		return nil, fmt.Errorf("invalid player2 id: %w", err)
	}

	// One transaction creates match + teams + participants + status + deadline.
	m, err := s.matchRepo.CreateMatchWithTeams(ctx, gameMode, "1v1", diff, pz, []*match.TeamSpec{
		{
			TeamNumber: 1,
			Participants: []match.ParticipantSpec{{PlayerID: &p1UUID, SlotNo: 1}},
		},
		{
			TeamNumber: 2,
			Participants: []match.ParticipantSpec{{PlayerID: &p2UUID, SlotNo: 1}},
		},
	}, time.Now().UTC().Add(s.cfg.MatchMaxDuration))
	if err != nil {
		return nil, err
	}

	// Set active match locks. If these fail (Redis down) the DB fallback in
	// HasActiveMatch still prevents double-matching.
	_, _ = activematch.Lock(ctx, s.redis.Client, player1ID, m.ID.String(), s.lockTTL())
	_, _ = activematch.Lock(ctx, s.redis.Client, player2ID, m.ID.String(), s.lockTTL())

	event := &MatchCreatedEvent{
		MatchID:     m.ID.String(),
		PlayerIDs:   []string{player1ID, player2ID},
		GameMode:    gameMode,
		MatchFormat: "1v1",
	}

	s.publishMatchCreated(ctx, event)
	return event, nil
}

func (s *Service) createBotMatch(ctx context.Context, player1ID string, player2ID *string, gameMode string, diff puzzle.Difficulty, botDiff bot.Difficulty) (*MatchCreatedEvent, error) {
	pz, err := s.generator.Generate(diff)
	if err != nil {
		return nil, fmt.Errorf("failed to generate puzzle: %w", err)
	}

	p1UUID, err := uuid.Parse(player1ID)
	if err != nil {
		return nil, fmt.Errorf("invalid player1 id: %w", err)
	}

	humanTeam := &match.TeamSpec{
		TeamNumber:   1,
		Participants: []match.ParticipantSpec{{PlayerID: &p1UUID, SlotNo: 1}},
	}
	playersList := []string{player1ID}

	if player2ID != nil {
		p2UUID, err := uuid.Parse(*player2ID)
		if err != nil {
			return nil, fmt.Errorf("invalid player2 id: %w", err)
		}
		humanTeam.Participants = append(humanTeam.Participants, match.ParticipantSpec{PlayerID: &p2UUID, SlotNo: 2})
		playersList = append(playersList, *player2ID)
	}

	// One transaction creates match + teams + participants + status + deadline.
	m, err := s.matchRepo.CreateMatchWithTeams(ctx, gameMode, "1_team_vs_bot", diff, pz, []*match.TeamSpec{
		humanTeam,
		{
			TeamNumber:    2,
			IsBotTeam:     true,
			BotDifficulty: &botDiff,
			Participants:  []match.ParticipantSpec{{IsBot: true, SlotNo: 1}},
		},
	}, time.Now().UTC().Add(s.cfg.MatchMaxDuration))
	if err != nil {
		return nil, err
	}

	for _, pID := range playersList {
		_, _ = activematch.Lock(ctx, s.redis.Client, pID, m.ID.String(), s.lockTTL())
	}

	event := &MatchCreatedEvent{
		MatchID:     m.ID.String(),
		PlayerIDs:   playersList,
		GameMode:    gameMode,
		MatchFormat: "1_team_vs_bot",
	}

	s.publishMatchCreated(ctx, event)
	return event, nil
}

func (s *Service) publishMatchCreated(ctx context.Context, event *MatchCreatedEvent) {
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return
	}
	s.redis.Publish(ctx, "matchmaking:match_created", string(eventBytes))
}

func containsString(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func containsDifficulty(list []puzzle.Difficulty, v puzzle.Difficulty) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

func containsBotDifficulty(list []bot.Difficulty, v bot.Difficulty) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
