package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/h3x02/suduxapi/internal/auth"
	"github.com/h3x02/suduxapi/internal/config"
	"github.com/h3x02/suduxapi/internal/email"
	"github.com/h3x02/suduxapi/internal/friendship"
	"github.com/h3x02/suduxapi/internal/logger"
	"github.com/h3x02/suduxapi/internal/match"
	"github.com/h3x02/suduxapi/internal/matchmaking"
	"github.com/h3x02/suduxapi/internal/middleware"
	"github.com/h3x02/suduxapi/internal/player"
	"github.com/h3x02/suduxapi/internal/postgres"
	"github.com/h3x02/suduxapi/internal/puzzle"
	"github.com/h3x02/suduxapi/internal/redis"
	"github.com/h3x02/suduxapi/internal/stats"
	"github.com/h3x02/suduxapi/internal/websocket"
)

func main() {
	cfg := config.Load()
	logger.Init(cfg.AppEnv)
	if err := cfg.Validate(); err != nil {
		logger.Log.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	logger.Log.Info("Starting Sudux Unified Server", "port", cfg.AppPort, "env", cfg.AppEnv)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 1. Connect Postgres & run migrations
	db, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Log.Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := postgres.RunMigrations(cfg.DatabaseURL, "migrations"); err != nil {
		logger.Log.Warn("migrations warning/notice", "err", err)
	}

	// 2. Connect Redis
	rdb, err := redis.Connect(ctx, cfg.RedisURL)
	if err != nil {
		logger.Log.Error("failed to connect to redis", "err", err)
		os.Exit(1)
	}

	// 3. Email sender setup
	var emailSender email.EmailSender
	if cfg.ResendAPIKey != "" && cfg.ResendAPIKey != "re_xxxxxxxxx" {
		emailSender = email.NewResendEmailSender(cfg.ResendAPIKey, cfg.ResendFromEmail)
	} else {
		emailSender = email.NewDevEmailSender()
	}

	// 4. Repositories & Services
	playerRepo := player.NewRepository(db)
	friendRepo := friendship.NewRepository(db)
	matchRepo := match.NewRepository(db)
	statsRepo := stats.NewRepository(db)

	authSvc := auth.NewService(cfg, playerRepo, rdb, emailSender)
	matchmakingSvc := matchmaking.NewService(rdb, matchRepo, cfg)

	// 5. WebSocket Hub setup
	hub := websocket.NewHub(matchRepo, cfg)
	hub.SetRedis(rdb.Typed())
	go hub.Run()

	// 6. Start Matchmaker background loops
	go workerLoop(ctx, rdb, matchmakingSvc)
	go deadlineJanitorLoop(ctx, matchRepo)
	go matchCreatedSubscriberLoop(ctx, rdb)

	// 7. Handlers setup
	authHandler := auth.NewHandler(authSvc, playerRepo)
	playerHandler := player.NewHandler(playerRepo)
	friendHandler := friendship.NewHandler(friendRepo)
	matchmakingHandler := matchmaking.NewHandler(matchmakingSvc)
	statsHandler := stats.NewHandler(statsRepo)

	// 8. Router setup
	r := chi.NewRouter()
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// WebSocket route (must not be wrapped in Timeout middleware)
	r.HandleFunc("/ws", hub.HandleWS)

	// REST API group with timeout
	r.Group(func(r chi.Router) {
		r.Use(chiMiddleware.Timeout(30 * time.Second))

		// Auth routes
		r.Post("/auth/request-code", authHandler.RequestCode)
		r.Post("/auth/verify-code", authHandler.VerifyCode)
		r.Post("/auth/refresh", authHandler.Refresh)
		r.Post("/auth/logout", authHandler.Logout)

		// Protected routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.AuthMiddleware(cfg.JWTAccessSecret))

			// Player
			r.Get("/me", playerHandler.GetMe)
			r.Patch("/me", playerHandler.UpdateMe)

			// Friends
			r.Get("/friends", friendHandler.GetFriends)
			r.Post("/friends/requests", friendHandler.SendRequest)
			r.Post("/friends/requests/{id}/accept", friendHandler.AcceptRequest)
			r.Post("/friends/requests/{id}/reject", friendHandler.RejectRequest)
			r.Delete("/friends/{id}", friendHandler.RemoveFriend)
			r.Post("/friends/{id}/block", friendHandler.BlockPlayer)

			// Matchmaking
			r.Post("/matchmaking/join", matchmakingHandler.JoinQueue)
			r.Post("/matchmaking/leave", matchmakingHandler.LeaveQueue)
			r.Get("/matchmaking/status", matchmakingHandler.GetStatus)

			// Stats
			r.Get("/stats", statsHandler.GetMyStats)
		})
	})

	srv := &http.Server{
		Addr:              ":" + cfg.AppPort,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0, // Websockets need long-lived reads; deadlines managed by ping/pong
		WriteTimeout:      0,
	}

	go func() {
		logger.Log.Info("Unified server listening on http://localhost:" + cfg.AppPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Log.Error("Unified server failed to start", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Log.Info("Shutting down unified server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Log.Error("graceful shutdown error", "err", err)
	}
}

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
						rdb.RPush(ctx, queueKey, p1ID)
						continue
					}

					logger.Log.Info("Matching players in matchmaker worker", "p1", p1ID, "p2", p2ID, "format", f)

					event, err := svc.Create1v1MatchExternal(ctx, p1ID, p2ID, m, d)
					if err != nil {
						logger.Log.Error("failed to create match — requeueing players",
							"p1", p1ID, "p2", p2ID, "err", err)
						rdb.RPush(ctx, queueKey, p1ID, p2ID)
						continue
					}

					rdb.Del(ctx, fmt.Sprintf("matchmaking:player:%s", p1ID))
					rdb.Del(ctx, fmt.Sprintf("matchmaking:player:%s", p2ID))
					_ = event
				}
			}
		}
	}
}

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

		exists, err := rdb.Exists(ctx, fmt.Sprintf("matchmaking:player:%s", playerID)).Result()
		if err != nil || exists == 0 {
			logger.Log.Info("discarding stale queue entry", "player_id", playerID)
			continue
		}
		return playerID, true
	}
}

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

func matchCreatedSubscriberLoop(ctx context.Context, rdb *redis.Client) {
	pubsub := rdb.Subscribe(ctx, "matchmaking:match_created")
	defer pubsub.Close()

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var event matchmaking.MatchCreatedEvent
			if err := json.Unmarshal([]byte(msg.Payload), &event); err == nil {
				logger.Log.Info("Match created event received", "match_id", event.MatchID, "players", event.PlayerIDs)
			}
		}
	}
}
