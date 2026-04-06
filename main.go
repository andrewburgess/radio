package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"andrewburgess.io/radio/config"
	"andrewburgess.io/radio/librespot"
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

	lp := librespot.New(librespot.Config{
		BinPath:    cfg.LibrespotBin,
		DeviceName: cfg.LibrespotDeviceName,
		CacheDir:   cfg.LibrespotCacheDir,
	})
	lp.Start()
	defer lp.Stop()

	srv, err := server.New(cfg)
	if err != nil {
		slog.Error("failed to create server", "err", err)
		os.Exit(1)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		slog.Info("shutting down")
		lp.Stop()
		os.Exit(0)
	}()

	if err := srv.Start(); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
