package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"andrewburgess.io/radio/audio"
	"andrewburgess.io/radio/config"
	"andrewburgess.io/radio/events"
	"andrewburgess.io/radio/hardware"
	"andrewburgess.io/radio/librespot"
	"andrewburgess.io/radio/server"
	"andrewburgess.io/radio/spotify"
	"andrewburgess.io/radio/store"
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

	db, err := store.New(cfg.DBPath)
	if err != nil {
		slog.Error("failed to open database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	auth, err := spotify.NewAuth(
		cfg.SpotifyClientID,
		cfg.SpotifyClientSecret,
		cfg.SpotifyRedirectURI,
		db,
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

	staticAudio := audio.NewStatic(audio.Config{
		Bin:  cfg.StaticAudioBin,
		File: cfg.StaticAudioFile,
		Sink: cfg.StaticAudioSink,
	})
	// staticAudio.Start() / Stop() are called by station-switch logic (Phase 9).
	// Ensure it is stopped on shutdown.
	defer staticAudio.Stop()

	bus := events.New()
	go forwardLibrespotEvents(lp.Events, bus)

	watchers := []hardware.Watcher{
		hardware.NewDial(bus, cfg.DialI2CBus, cfg.DialI2CAddr, cfg.BucketCount, cfg.DialMinAngle, cfg.DialMaxAngle),
		hardware.NewToggle(bus, cfg.ToggleGPIOPin),
		hardware.NewPower(bus, cfg.PowerGPIOPin),
		hardware.NewVolume(bus, cfg.VolumeSPIDev, cfg.VolumeSPIChannel, cfg.AlsaMixerControl),
	}
	for _, w := range watchers {
		if err := w.Start(); err != nil {
			slog.Error("failed to start hardware watcher", "err", err)
			os.Exit(1)
		}
		defer w.Stop()
	}

	srv, err := server.New(cfg, spotifyClient, db, bus)
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

// forwardLibrespotEvents translates librespot events into bus events.
func forwardLibrespotEvents(in <-chan librespot.Event, bus *events.Bus) {
	for e := range in {
		switch e.Type {
		case librespot.EventTrackChanged:
			bus.Publish(events.Event{
				Kind:       events.KindTrackChanged,
				TrackURI:   e.URI,
				TrackName:  e.Name,
				Artists:    e.Artists,
				Album:      e.Album,
				ShowName:   e.ShowName,
				DurationMs: e.DurationMs,
			})
		case librespot.EventPlaying:
			bus.Publish(events.Event{
				Kind:       events.KindPlaybackStateChanged,
				Playing:    true,
				PositionMs: e.PositionMs,
				TrackURI:   e.TrackID,
			})
		case librespot.EventPaused, librespot.EventStopped:
			bus.Publish(events.Event{
				Kind:       events.KindPlaybackStateChanged,
				Playing:    false,
				PositionMs: e.PositionMs,
				TrackURI:   e.TrackID,
			})
		}
	}
}

