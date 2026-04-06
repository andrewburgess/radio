package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"andrewburgess.io/radio/config"
	"andrewburgess.io/radio/librespot"
	"andrewburgess.io/radio/server"
	"andrewburgess.io/radio/spotify"
)

func main() {
	// When librespot fires an event it spawns this binary as the --onevent
	// handler. Detect that case via the PLAYER_EVENT env var librespot sets,
	// forward the event to the main process over the Unix socket, and exit.
	if os.Getenv("PLAYER_EVENT") != "" {
		librespot.RunEventForwarder()
		return
	}

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

	tokenStore := spotify.NewFileTokenStore(cfg.SpotifyTokenFile)
	auth, err := spotify.NewAuth(
		cfg.SpotifyClientID,
		cfg.SpotifyClientSecret,
		cfg.SpotifyRedirectURI,
		tokenStore,
	)
	if err != nil {
		slog.Error("failed to initialize Spotify auth", "err", err)
		os.Exit(1)
	}
	if !auth.HasToken() {
		slog.Warn("Spotify not authorized — visit /auth in a browser to complete setup")
	}
	spotifyClient := spotify.NewClient(auth)

	lp := librespot.New(librespot.Config{
		BinPath:    cfg.LibrespotBin,
		DeviceName: cfg.LibrespotDeviceName,
		CacheDir:   cfg.LibrespotCacheDir,
	})
	if err := lp.Start(); err != nil {
		slog.Error("failed to start librespot", "err", err)
		os.Exit(1)
	}
	defer lp.Stop()

	srv, err := server.New(cfg, spotifyClient)
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
