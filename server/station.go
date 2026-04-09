package server

import (
	"context"
	"log/slog"
	"time"

	"andrewburgess.io/radio/events"
	"andrewburgess.io/radio/spotify"
)

// runStationController subscribes to the event bus and drives playback:
//   - power on    → switch to current station
//   - power off   → stop everything
//   - dial moved  → switch station (if powered on)
//   - mode toggle → switch station (if powered on)
//
// It maintains its own local copy of bucket/mode/powered so there is no race
// with runStateUpdater reading from the same bus subscription.
func (s *Server) runStationController() {
	ch := s.bus.Subscribe()
	defer s.bus.Unsubscribe(ch)

	var (
		bucket  int
		mode    = events.ModeMusic
		powered bool
	)

	for e := range ch {
		switch e.Kind {
		case events.KindDialMoved:
			bucket = e.Bucket
			if powered {
				go s.switchStation(bucket, string(mode))
			}
		case events.KindToggleSwitched:
			mode = e.Mode
			if powered {
				go s.switchStation(bucket, string(mode))
			}
		case events.KindPowerChanged:
			powered = e.PowerOn
			if powered {
				go s.switchStation(bucket, string(mode))
			} else {
				go s.stopPlayback()
			}
		}
	}
}

// switchStation looks up the station for bucket/mode and either starts Spotify
// playback at the correct radio-time position or falls back to static audio.
func (s *Server) switchStation(bucket int, mode string) {
	ctx := context.Background()

	station, err := s.store.GetStation(bucket, mode)
	if err != nil {
		slog.Error("station: get station", "bucket", bucket, "mode", mode, "err", err)
		return
	}

	if station == nil || station.PlaylistURI == "" {
		slog.Info("station: no assignment — playing static", "bucket", bucket, "mode", mode)
		if err := s.spotify.Pause(ctx, ""); err != nil {
			slog.Debug("station: pause before static", "err", err)
		}
		s.staticAudio.Start()
		s.bus.Publish(events.Event{Kind: events.KindStaticStarted})
		return
	}

	s.staticAudio.Stop()
	s.bus.Publish(events.Event{Kind: events.KindStaticStopped})

	tracks, err := s.spotify.GetTracksWithCache(ctx, station.PlaylistURI, s.store)
	if err != nil {
		slog.Error("station: fetch tracks", "uri", station.PlaylistURI, "err", err)
		s.staticAudio.Start()
		s.bus.Publish(events.Event{Kind: events.KindStaticStarted})
		return
	}
	if len(tracks) == 0 {
		slog.Warn("station: empty playlist — playing static", "uri", station.PlaylistURI)
		s.staticAudio.Start()
		s.bus.Publish(events.Event{Kind: events.KindStaticStarted})
		return
	}

	trackIdx, posMs := radioTimeOffset(tracks)
	slog.Info("station: switching",
		"bucket", bucket, "mode", mode,
		"uri", station.PlaylistURI,
		"track_idx", trackIdx, "pos_ms", posMs,
	)

	if err := s.spotify.Play(ctx, "", station.PlaylistURI, trackIdx, posMs); err != nil {
		slog.Error("station: play", "err", err)
	}
}

// stopPlayback pauses Spotify and stops static audio.
func (s *Server) stopPlayback() {
	s.staticAudio.Stop()
	s.bus.Publish(events.Event{Kind: events.KindStaticStopped})
	if err := s.spotify.Pause(context.Background(), ""); err != nil {
		slog.Debug("station: pause on power off", "err", err)
	}
}

// radioTimeOffset computes (trackIndex, positionMs) for "radio time": the
// current position in the playlist is derived from Unix epoch milliseconds
// modulo the total playlist duration. All listeners who switch to the same
// station at the same wall-clock time hear the same point in the playlist.
func radioTimeOffset(tracks []spotify.Track) (int, int) {
	var totalMs int64
	for _, t := range tracks {
		totalMs += int64(t.DurationMs)
	}
	if totalMs == 0 {
		return 0, 0
	}

	pos := time.Now().UnixMilli() % totalMs
	var acc int64
	for i, t := range tracks {
		end := acc + int64(t.DurationMs)
		if pos < end {
			return i, int(pos - acc)
		}
		acc = end
	}
	return 0, 0
}
