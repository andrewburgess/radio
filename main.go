package main

import (
	"log/slog"
	"os"

	"andrewburgess.io/radio/config"
	"andrewburgess.io/radio/server"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	slog.Info("config loaded",
		"port", cfg.Port,
		"bucket_count", cfg.BucketCount,
		"db_path", cfg.DBPath,
	)

	srv, err := server.New(cfg)
	if err != nil {
		slog.Error("failed to create server", "err", err)
		os.Exit(1)
	}

	if err := srv.Start(); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
