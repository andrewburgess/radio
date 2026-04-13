package server

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
			if powered && mode != events.ModeSpeaker {
				go s.switchStation(bucket, string(mode))
			}
		case events.KindToggleSwitched:
			mode = e.Mode
			if powered {
				if mode == events.ModeSpeaker {
					// AFC position: hand off to Spotify Connect passively.
					go s.enterSpeakerMode()
				} else {
					go s.switchStation(bucket, string(mode))
				}
			}
		case events.KindPowerChanged:
			powered = e.PowerOn
			if powered {
				if err := s.librespot.Start(); err != nil {
					slog.Error("station: start librespot", "err", err)
				}
				if mode == events.ModeSpeaker {
					go s.enterSpeakerMode()
				} else {
					go s.switchStation(bucket, string(mode))
				}
			} else {
				go s.stopPlayback()
			}
		case events.KindTrackEnded:
			// Music playlists advance automatically via Spotify's context; podcast
			// episodes are played as single URIs so we must advance manually.
			if powered && mode == events.ModePodcast {
				go s.switchStation(bucket, string(mode))
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
		s.amp.Unmute()
		s.staticAudio.Start()
		s.bus.Publish(events.Event{Kind: events.KindStaticStarted})
		return
	}

	s.staticAudio.Stop()
	s.bus.Publish(events.Event{Kind: events.KindStaticStopped})

	stationName, stationImage, err := s.spotify.GetPlaylistInfo(ctx, station.PlaylistURI)
	if err != nil {
		slog.Warn("station: fetch playlist info", "uri", station.PlaylistURI, "err", err)
	}
	s.bus.Publish(events.Event{
		Kind:            events.KindStationChanged,
		StationName:     stationName,
		StationImageURL: stationImage,
	})

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

	deviceID, err := s.findDevice(ctx)
	if err != nil {
		slog.Error("station: find device", "err", err)
		// fall through with empty ID — Spotify targets the last active device
	}

	if err := s.spotify.SetVolume(ctx, deviceID, 100); err != nil {
		slog.Warn("station: set volume", "err", err)
	}

	trackIdx, posMs := radioTimeOffset(tracks)
	trackURI := tracks[trackIdx].URI
	slog.Info("station: switching",
		"bucket", bucket, "mode", mode,
		"uri", station.PlaylistURI,
		"device_id", deviceID,
		"track_idx", trackIdx, "pos_ms", posMs,
	)

	s.amp.Unmute()

	var playErr error
	if strings.HasPrefix(trackURI, "spotify:episode:") {
		playErr = s.spotify.PlayEpisode(ctx, deviceID, trackURI, posMs)
	} else {
		playErr = s.spotify.Play(ctx, deviceID, station.PlaylistURI, trackIdx, posMs)
	}
	if playErr != nil {
		slog.Error("station: play", "err", playErr)
	}
}

// findDevice returns the Spotify Connect device ID for the configured librespot
// instance. It retries several times with a short delay because librespot may
// take a moment to register with Spotify after starting.
func (s *Server) findDevice(ctx context.Context) (string, error) {
	const attempts = 6
	const retryDelay = 500 * time.Millisecond

	for i := range attempts {
		if i > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(retryDelay):
			}
			slog.Debug("station: retrying device lookup", "attempt", i+1)
		}

		devices, err := s.spotify.GetDevices(ctx)
		if err != nil {
			return "", fmt.Errorf("get devices: %w", err)
		}

		for _, d := range devices {
			if d.Name == s.cfg.LibrespotDeviceName {
				return d.ID, nil
			}
		}
	}

	return "", fmt.Errorf("device %q not found after %d attempts", s.cfg.LibrespotDeviceName, attempts)
}

// enterSpeakerMode stops radio-controlled playback and unmutes the amp so
// Spotify Connect (phone/tablet) can drive the speaker directly.
func (s *Server) enterSpeakerMode() {
	s.staticAudio.Stop()
	s.bus.Publish(events.Event{Kind: events.KindStaticStopped})
	if err := s.spotify.Pause(context.Background(), ""); err != nil {
		slog.Debug("station: pause before speaker mode", "err", err)
	}
	s.amp.Unmute()
	slog.Info("station: speaker mode — radio control suspended")
}

// stopPlayback pauses Spotify, stops static audio, mutes the amp, and shuts
// down librespot so the device disappears from Spotify Connect.
func (s *Server) stopPlayback() {
	s.staticAudio.Stop()
	s.bus.Publish(events.Event{Kind: events.KindStaticStopped})
	if err := s.spotify.Pause(context.Background(), ""); err != nil {
		slog.Debug("station: pause on power off", "err", err)
	}
	s.amp.Mute()
	s.librespot.Stop()
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
