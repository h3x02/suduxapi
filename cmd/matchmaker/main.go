package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/h3x02/suduxapi/internal/bot"
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
	logger.Init(cfg.AppEnv)

	logger.Log.Info("Matchmaker Server starting...", "env", cfg.AppEnv)

	ctx := context.Background()

	db, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Log.Error("failed to connect to postgres", "err", err)
		return
	}
	defer db.Close()

	rdb, err := redis.Connect(ctx, cfg.RedisURL)
	if err != nil {
		logger.Log.Error("failed to connect to redis", "err", err)
		return
	}

	matchRepo := match.NewRepository(db)
	matchmakingSvc := matchmaking.NewService(rdb, matchRepo)

	logger.Log.Info("Matchmaker worker processing matchmaking queues...")

	// Worker loop to pop from Redis queues and match players
	go func() {
		modes := []string{"cell_race"}
		formats := []string{"1v1", "1_team_vs_bot"}
		diffs := []puzzle.Difficulty{puzzle.DifficultyEasy, puzzle.DifficultyMedium, puzzle.DifficultyHard, puzzle.DifficultyExpert}

		for {
			time.Sleep(1 * time.Second)
			for _, m := range modes {
				for _, f := range formats {
					for _, d := range diffs {
						queueKey := fmt.Sprintf("matchmaking:queue:%s:%s:%s", m, f, d)
						p1ID, err := rdb.LPop(ctx, queueKey).Result()
						if err != nil || p1ID == "" {
							continue
						}

						p2ID, err := rdb.LPop(ctx, queueKey).Result()
						if err != nil || p2ID == "" {
							// Put p1 back if p2 not found
							rdb.LPush(ctx, queueKey, p1ID)
							continue
						}

						// Found match between p1 and p2!
						logger.Log.Info("Matching players in matchmaker worker", "p1", p1ID, "p2", p2ID, "format", f)
						rdb.Del(ctx, fmt.Sprintf("matchmaking:player:%s", p1ID))
						rdb.Del(ctx, fmt.Sprintf("matchmaking:player:%s", p2ID))

						if f == "1v1" {
							_, _ = matchmakingSvc.Create1v1MatchExternal(ctx, p1ID, p2ID, m, d)
						} else {
							botDiff := bot.DifficultyNoob
							_, _ = matchmakingSvc.CreateBotMatchExternal(ctx, p1ID, &p2ID, m, d, botDiff)
						}
					}
				}
			}
		}
	}()

	pubsub := rdb.Subscribe(ctx, "matchmaking:match_created")
	defer pubsub.Close()

	ch := pubsub.Channel()
	for msg := range ch {
		var event matchmaking.MatchCreatedEvent
		if err := json.Unmarshal([]byte(msg.Payload), &event); err == nil {
			logger.Log.Info("Match created event received", "match_id", event.MatchID, "players", event.PlayerIDs)
		}
	}
}
