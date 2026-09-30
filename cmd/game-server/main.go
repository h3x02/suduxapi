package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/h3x02/suduxapi/internal/config"
	"github.com/h3x02/suduxapi/internal/logger"
	"github.com/h3x02/suduxapi/internal/match"
	"github.com/h3x02/suduxapi/internal/postgres"
	"github.com/h3x02/suduxapi/internal/redis"
	"github.com/h3x02/suduxapi/internal/websocket"
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

	logger.Log.Info("Game Server starting...", "port", cfg.AppPort, "env", cfg.AppEnv)

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
	hub := websocket.NewHub(matchRepo, cfg)
	hub.SetRedis(rdb.Typed())
	go hub.Run()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", hub.HandleWS)

	srv := &http.Server{
		Addr:         ":" + cfg.AppPort,
		Handler:      mux,
		ReadTimeout:  0, // websockets need long-lived reads; deadlines are managed by pong/ping
		WriteTimeout: 0,
	}

	go func() {
		logger.Log.Info("Game server listening on port " + cfg.AppPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Log.Error("Game server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Log.Info("Shutting down game server...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Log.Error("graceful shutdown", "err", err)
	}
}
