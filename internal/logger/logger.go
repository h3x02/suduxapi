package logger

import (
	"log/slog"
	"os"
	"strings"
)

var Log *slog.Logger

func Init(env string) {
	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}
	if strings.ToLower(env) == "development" || env == "dev" {
		opts.Level = slog.LevelDebug
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	Log = slog.New(handler)
	slog.SetDefault(Log)
}
