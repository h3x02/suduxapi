package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/h3x02/suduxapi/internal/config"
	"github.com/h3x02/suduxapi/internal/logger"
	"github.com/h3x02/suduxapi/internal/match"
	"github.com/h3x02/suduxapi/internal/matchmaking"
	"github.com/h3x02/suduxapi/internal/postgres"
	"github.com/h3x02/suduxapi/internal/puzzle"
	"github.com/h3x02/suduxapi/internal/redis"
)

func main() {
	cfg := config.Load()
	// Init logging BEFORE validation: logging a config error through a nil
	// logger used to panic with a segfault instead of printing the problem.
	logger.Init(cfg.AppEnv)
	if err := cfg.Validate(); err != nil {
		logger.Log.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	logger.Log.Info("Matchmaker Server starting...", "env", cfg.AppEnv)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Log.Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	rdb, err := redis.Connect(ctx, cfg.RedisURL)
	if err != nil {
		logger.Log.Error("failed to connect to redis", "err", err)
		os.Exit(1)
	}

	matchRepo := match.NewRepository(db)
	matchmakingSvc := matchmaking.NewService(rdb, matchRepo, cfg)

	logger.Log.Info("Matchmaker worker processing matchmaking queues...")

	go workerLoop(ctx, rdb, matchmakingSvc)
	go deadlineJanitorLoop(ctx, matchRepo)

	// Subscribe to match-created events purely for observability/logging.
	pubsub := rdb.Subscribe(ctx, "matchmaking:match_created")
	defer pubsub.Close()

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			logger.Log.Info("Matchmaker shutting down...")
			return
		case msg := <-ch:
			var event matchmaking.MatchCreatedEvent
			if err := json.Unmarshal([]byte(msg.Payload), &event); err == nil {
				logger.Log.Info("Match created event received", "match_id", event.MatchID, "players", event.PlayerIDs)
			}
		}
	}
}

// workerLoop pops player IDs from the queue and pairs them.
//
// Concurrency-safety: after LPop we re-check the player's queue key
// (matchmaking:player:{id}). LeaveQueue deletes that key, so a player who left
// between enqueue and pop is detected here instead of being force-matched —
// the old code popped two players non-atomically and created broken matches
// for players who had already left, or dropped them silently on errors.
func workerLoop(ctx context.Context, rdb *redis.Client, svc *matchmaking.Service) {
	modes := []string{"cell_race"}
	formats := []string{"1v1"}
	diffs := []puzzle.Difficulty{puzzle.DifficultyEasy, puzzle.DifficultyMedium, puzzle.DifficultyHard, puzzle.DifficultyExpert}

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(1 * time.Second):
		}

		for _, m := range modes {
			for _, f := range formats {
				for _, d := range diffs {
					queueKey := fmt.Sprintf("matchmaking:queue:%s:%s:%s", m, f, d)

					p1ID, ok := popValidPlayer(ctx, rdb, queueKey)
					if !ok {
						continue
					}

					p2ID, ok := popValidPlayer(ctx, rdb, queueKey)
					if !ok {
						// Put p1 back if p2 not found.
						rdb.RPush(ctx, queueKey, p1ID)
						continue
					}

					logger.Log.Info("Matching players in matchmaker worker", "p1", p1ID, "p2", p2ID, "format", f)

					// "1_team_vs_bot" is instant-created on join (solo or with
					// a friend) and never enters the queue, so only 1v1 pairs
					// are matched here.
					event, err := svc.Create1v1MatchExternal(ctx, p1ID, p2ID, m, d)
					if err != nil {
						logger.Log.Error("failed to create match — requeueing players",
							"p1", p1ID, "p2", p2ID, "err", err)
						// Player queue keys still exist (deletion happens after
						// success), so re-pushed entries pass popValidPlayer.
						rdb.RPush(ctx, queueKey, p1ID, p2ID)
						continue
					}

					// Success: clear queue markers so popValidPlayer and
					// LeaveQueue see the players as matched.
					rdb.Del(ctx, fmt.Sprintf("matchmaking:player:%s", p1ID))
					rdb.Del(ctx, fmt.Sprintf("matchmaking:player:%s", p2ID))
					_ = event
				}
			}
		}
	}
}

// popValidPlayer pops a player ID and verifies they are still queued (their
// player key still exists). Players who left the queue are discarded.
func popValidPlayer(ctx context.Context, rdb *redis.Client, queueKey string) (string, bool) {
	for {
		select {
		case <-ctx.Done():
			return "", false
		default:
		}

		playerID, err := rdb.LPop(ctx, queueKey).Result()
		if err != nil || playerID == "" {
			return "", false
		}

		// Re-validate queue membership after pop.
		exists, err := rdb.Exists(ctx, fmt.Sprintf("matchmaking:player:%s", playerID)).Result()
		if err != nil || exists == 0 {
			logger.Log.Info("discarding stale queue entry", "player_id", playerID)
			continue
		}
		return playerID, true
	}
}

// deadlineJanitorLoop cancels matches whose deadline passed even if no game
// server ever loaded them (e.g. nobody connected).
func deadlineJanitorLoop(ctx context.Context, matchRepo *match.Repository) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expired, err := matchRepo.FinishExpiredMatches(ctx, time.Now())
			if err != nil {
				logger.Log.Warn("matchmaker deadline janitor failed", "err", err)
				continue
			}
			for _, id := range expired {
				logger.Log.Info("cancelled expired match", "match_id", id)
			}
		}
	}
}
