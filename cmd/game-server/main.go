package main

import (
	"context"
	"net/http"

	"github.com/h3x02/suduxapi/internal/config"
	"github.com/h3x02/suduxapi/internal/logger"
	"github.com/h3x02/suduxapi/internal/match"
	"github.com/h3x02/suduxapi/internal/postgres"
	"github.com/h3x02/suduxapi/internal/websocket"
)

func main() {
	cfg := config.Load()
	logger.Init(cfg.AppEnv)

	logger.Log.Info("Game Server starting...", "port", cfg.AppPort, "env", cfg.AppEnv)

	ctx := context.Background()
	db, err := postgres.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Log.Error("failed to connect to postgres", "err", err)
		return
	}
	defer db.Close()

	matchRepo := match.NewRepository(db)
	hub := websocket.NewHub(matchRepo, cfg.JWTAccessSecret)
	go hub.Run()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", hub.HandleWS)

	logger.Log.Info("Game Server listening on port " + cfg.AppPort)
	if err := http.ListenAndServe(":"+cfg.AppPort, mux); err != nil {
		logger.Log.Error("Game server failed", "err", err)
	}
}
