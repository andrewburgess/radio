package server

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"sync/atomic"
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
		bucket              int
		mode                = events.ModeMusic
		powered             bool
		assigned            bool    // true when current bucket has a playlist
		tuneQuality         float64 // latest value from KindTuneQualityChanged
		prevQuality         float64 // previous value, used to detect sweet-spot entry
		currentPlaylistURI  string
		interstitialSongs   = make(map[string]int) // songs since last interstitial, per playlist URI
		interstitialTimer   *time.Timer            // pending pre-end-of-track trigger
		interstitialRunning atomic.Bool            // true while a clip is actively playing

		cancelSwitch       context.CancelFunc = func() {}
		cancelInterstitial context.CancelFunc = func() {}
	)

	// cancelAll stops any in-flight station switch, pending interstitial timer,
	// and any currently playing interstitial.
	cancelAll := func() {
		cancelSwitch()
		cancelInterstitial()
		cancelInterstitial = func() {}
		if interstitialTimer != nil {
			interstitialTimer.Stop()
			interstitialTimer = nil
		}
	}

	// startSwitch cancels any in-flight switchStation goroutine then launches a
	// new one. This ensures rapid dial movement doesn't stack up concurrent
	// Spotify API calls that race to be the last writer.
	startSwitch := func(b int, m string) {
		cancelAll()
		ctx, cancel := context.WithCancel(context.Background())
		cancelSwitch = cancel
		go s.switchStation(ctx, b, m)
	}

	for e := range ch {
		switch e.Kind {
		case events.KindDialMoved:
			bucket = e.Bucket
			if powered && mode != events.ModeSpeaker {
				startSwitch(bucket, string(mode))
			}

		case events.KindTuneQualityChanged:
			if !powered || mode == events.ModeSpeaker {
				break
			}
			prevQuality = tuneQuality
			tuneQuality = e.TuneQuality
			if assigned {
				s.staticAudio.SetGain(s.staticGain(tuneQuality))
				// Entering the sweet spot: shuffle to a fresh static position.
				if tuneQuality >= 1.0 && prevQuality < 1.0 {
					s.staticAudio.Shuffle()
				}
			}

		case events.KindStaticStarted:
			assigned = false
			// Static is at full volume for unassigned buckets; gain is managed
			// by switchStation directly so nothing extra to do here.

		case events.KindStaticStopped:
			assigned = true
			// Snap gain to match the current tune quality immediately.
			s.staticAudio.SetGain(s.staticGain(tuneQuality))

		case events.KindToggleSwitched:
			mode = e.Mode
			if powered {
				if mode == events.ModeSpeaker {
					cancelAll()
					go s.enterSpeakerMode()
				} else {
					startSwitch(bucket, string(mode))
				}
			}

		case events.KindTrackChanged:
			// Stop any pending timer. Do NOT cancel a running interstitial — let it
			// play through the track boundary naturally.
			if interstitialTimer != nil {
				interstitialTimer.Stop()
				interstitialTimer = nil
			}
			// Schedule an interstitial 4 s before this track ends (music mode only,
			// and only when no clip is already playing).
			if powered && mode == events.ModeMusic && currentPlaylistURI != "" && !interstitialRunning.Load() {
				if s.interstitials.HasClips(currentPlaylistURI) {
					songs := interstitialSongs[currentPlaylistURI] + 1
					interstitialSongs[currentPlaylistURI] = songs
					chance := float64(songs) * s.cfg.InterstitialChanceIncrement / 100.0
					if rand.Float64() < chance {
						interstitialSongs[currentPlaylistURI] = 0
						delay := time.Duration(e.DurationMs-4000) * time.Millisecond
						if delay < 0 {
							delay = 0
						}
						playlistURI := currentPlaylistURI
						ctx, cancel := context.WithCancel(context.Background())
						cancelInterstitial = cancel
						interstitialTimer = time.AfterFunc(delay, func() {
							interstitialRunning.Store(true)
							defer interstitialRunning.Store(false)
							s.playInterstitial(ctx, playlistURI)
						})
					}
				}
			}

		case events.KindStationChanged:
			currentPlaylistURI = e.PlaylistURI

		case events.KindPowerChanged:
			powered = e.PowerOn
			if powered {
				if err := s.librespot.Start(); err != nil {
					slog.Error("station: start librespot", "err", err)
				}
				s.staticAudio.Start()
				if mode == events.ModeSpeaker {
					cancelAll()
					go s.enterSpeakerMode()
				} else {
					startSwitch(bucket, string(mode))
				}
			} else {
				// Cancel any in-flight switch and interstitial before tearing down.
				cancelAll()
				go s.stopPlayback()
			}

		case events.KindTrackEnded:
			// Music playlists advance automatically via Spotify's context; podcast
			// episodes are played as single URIs so we must advance manually.
			if powered && mode == events.ModePodcast {
				startSwitch(bucket, string(mode))
			}
		}
	}
}

// switchStation looks up the station for bucket/mode and either starts Spotify
// playback at the correct radio-time position or falls back to static audio.
// ctx is cancelled by runStationController when a newer switch supersedes this one.
const fadeDuration = 250 * time.Millisecond

func (s *Server) switchStation(ctx context.Context, bucket int, mode string) {
	s.librespot.FadeOut(ctx, fadeDuration)
	if ctx.Err() != nil {
		return
	}

	station, err := s.store.GetStation(bucket, mode)
	if err != nil {
		slog.Error("station: get station", "bucket", bucket, "mode", mode, "err", err)
		return
	}
	if ctx.Err() != nil {
		return
	}

	if station == nil || station.PlaylistURI == "" {
		slog.Info("station: no assignment — playing static", "bucket", bucket, "mode", mode)
		if err := s.spotify.Pause(ctx, ""); err != nil {
			slog.Debug("station: pause before static", "err", err)
		}
		s.amp.Unmute()
		// Full volume for unassigned buckets; shuffle to a fresh position so
		// each empty station sounds different.
		s.staticAudio.SetGain(1.0)
		s.staticAudio.Shuffle()
		s.bus.Publish(events.Event{Kind: events.KindStaticStarted})
		return
	}

	// Assigned bucket: static stays running at a gain the station controller
	// will set based on tune quality. Publish KindStaticStopped so it knows.
	s.bus.Publish(events.Event{Kind: events.KindStaticStopped})

	stationName, stationImage, err := s.spotify.GetPlaylistInfo(ctx, station.PlaylistURI)
	if err != nil && ctx.Err() == nil {
		slog.Warn("station: fetch playlist info", "uri", station.PlaylistURI, "err", err)
	}
	if ctx.Err() != nil {
		return
	}
	s.bus.Publish(events.Event{
		Kind:            events.KindStationChanged,
		StationName:     stationName,
		StationImageURL: stationImage,
		PlaylistURI:     station.PlaylistURI,
	})

	tracks, err := s.spotify.GetTracksWithCache(ctx, station.PlaylistURI, s.store)
	if err != nil {
		slog.Error("station: fetch tracks", "uri", station.PlaylistURI, "err", err)
		s.staticAudio.SetGain(1.0)
		s.staticAudio.Shuffle()
		s.bus.Publish(events.Event{Kind: events.KindStaticStarted})
		return
	}
	if len(tracks) == 0 {
		slog.Info("station: empty playlist — playing static", "uri", station.PlaylistURI)
		s.staticAudio.SetGain(1.0)
		s.staticAudio.Shuffle()
		s.bus.Publish(events.Event{Kind: events.KindStaticStarted})
		return
	}
	if ctx.Err() != nil {
		return
	}

	deviceID, err := s.findDevice(ctx)
	if err != nil {
		if ctx.Err() != nil {
			slog.Debug("station: switch cancelled during device lookup", "bucket", bucket, "mode", mode)
			return
		}
		slog.Error("station: find device", "err", err)
		// fall through with empty ID — Spotify targets the last active device
	}
	if ctx.Err() != nil {
		return
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

	// Arm before Play so a fast EventPlaying fired between Play returning and
	// FadeIn being called is not missed.
	s.librespot.ArmFadeIn()

	var playErr error
	if strings.HasPrefix(trackURI, "spotify:episode:") {
		playErr = s.spotify.PlayEpisode(ctx, deviceID, trackURI, posMs)
	} else {
		playErr = s.spotify.Play(ctx, deviceID, station.PlaylistURI, trackIdx, posMs)
	}
	if playErr != nil {
		slog.Error("station: play", "err", playErr)
		return
	}

	s.librespot.FadeIn(ctx, fadeDuration)
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

// enterSpeakerMode suspends radio tuning control without interrupting playback.
// Whatever is currently playing continues; the dial is ignored until the toggle
// leaves AFC position.
func (s *Server) enterSpeakerMode() {
	slog.Info("station: speaker mode — tuning suspended, playback unchanged")
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

// staticGain converts a tune quality value (0–1) into a static audio gain.
// At quality=1 (sweet spot) the gain is 0 (silent). Below 1 the gain is
// floored to staticMinGain so static is immediately audible as soon as the
// dial leaves the sweet spot, then ramps to 1.0 at the bucket boundary.
func (s *Server) staticGain(quality float64) float64 {
	if quality >= 1.0 {
		return 0
	}
	gain := 1.0 - quality
	if gain < s.staticMinGain {
		gain = s.staticMinGain
	}
	return gain
}

// playInterstitial ducks the Spotify stream, plays a randomly selected clip
// from the interstitial library for playlistURI, then restores volume.
// If ctx is cancelled mid-clip (e.g. dial turn), the clip stops and volume is
// left at duck level — the subsequent FadeOut in switchStation will handle it
// cleanly because FadeOut reads currentVolPct rather than assuming 100%.
func (s *Server) playInterstitial(ctx context.Context, playlistURI string) {
	clip, err := s.interstitials.PickClip(playlistURI)
	if err != nil {
		slog.Debug("interstitial: pick clip", "err", err)
		return
	}

	const duckDuration = 150 * time.Millisecond
	s.librespot.Duck(ctx, s.interstitialDuckLevel, duckDuration)
	if ctx.Err() != nil {
		return
	}

	slog.Info("interstitial: playing", "playlist", playlistURI)
	if err := s.interstitials.Play(ctx, clip); err != nil && ctx.Err() == nil {
		slog.Debug("interstitial: play error", "err", err)
	}
	if ctx.Err() != nil {
		return
	}

	const unduckDuration = 200 * time.Millisecond
	s.librespot.Unduck(context.Background(), unduckDuration)
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
