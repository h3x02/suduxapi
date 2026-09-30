package main

import (
	"context"
	"errors"
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
	"github.com/h3x02/suduxapi/internal/redis"
	"github.com/h3x02/suduxapi/internal/stats"
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

	logger.Log.Info("Starting Sudux REST API server", "port", cfg.AppPort, "env", cfg.AppEnv)

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

	// Handlers
	authHandler := auth.NewHandler(authSvc, playerRepo)
	playerHandler := player.NewHandler(playerRepo)
	friendHandler := friendship.NewHandler(friendRepo)
	matchmakingHandler := matchmaking.NewHandler(matchmakingSvc)
	statsHandler := stats.NewHandler(statsRepo)

	// Router setup
	r := chi.NewRouter()
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)
	r.Use(chiMiddleware.Timeout(30 * time.Second))

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

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

	srv := &http.Server{
		Addr:         ":" + cfg.AppPort,
		Handler:      r,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		logger.Log.Info("Listening on http://localhost:" + cfg.AppPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Log.Error("Server failed to start", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Log.Info("Shutting down API server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Log.Error("graceful shutdown", "err", err)
	}
}
