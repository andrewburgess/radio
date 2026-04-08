package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"andrewburgess.io/radio/spotify"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.render(w, "index", "base", s.state.snapshot(s.cfg.BucketCount))
}

// handleAuth begins the Spotify OAuth flow by redirecting the user to the
// Spotify authorization page. Visit /auth once after first launch to authorize
// the application.
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	authURL, err := s.spotify.Auth().AuthURL()
	if err != nil {
		slog.Error("failed to build Spotify auth URL", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleAuthCallback handles the redirect from Spotify after the user
// approves (or denies) the authorization request.
func (s *Server) handleAuthCallback(w http.ResponseWriter, r *http.Request) {
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		slog.Warn("Spotify auth denied", "error", errParam)
		http.Error(w, "Spotify authorization denied: "+errParam, http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	if err := s.spotify.Auth().Exchange(r.Context(), code, state); err != nil {
		slog.Error("Spotify token exchange failed", "err", err)
		http.Error(w, "authorization failed", http.StatusInternalServerError)
		return
	}

	slog.Info("Spotify authorization complete")
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleDebugPlay is a Phase 2 test endpoint that exercises the full playback
// chain: find the librespot device, fetch playlist tracks, compute radio time,
// and issue a play command. Remove or gate this behind a build tag in production.
//
// Requires SPOTIFY_TEST_PLAYLIST to be set in the environment.
func (s *Server) handleDebugPlay(w http.ResponseWriter, r *http.Request) {
	if s.cfg.SpotifyTestPlaylist == "" {
		http.Error(w, "SPOTIFY_TEST_PLAYLIST not set", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	devices, err := s.spotify.GetDevices(ctx)
	if err != nil {
		slog.Error("debug/play: get devices failed", "err", err)
		http.Error(w, "get devices: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var deviceID string
	for _, d := range devices {
		if d.Name == s.cfg.LibrespotDeviceName {
			deviceID = d.ID
			break
		}
	}
	if deviceID == "" {
		http.Error(w, fmt.Sprintf("device %q not found — is librespot running?", s.cfg.LibrespotDeviceName),
			http.StatusServiceUnavailable)
		return
	}

	tracks, err := s.spotify.GetTracksWithCache(ctx, s.cfg.SpotifyTestPlaylist, s.playlistCache)
	if err != nil {
		slog.Error("debug/play: get playlist tracks failed", "err", err)
		http.Error(w, "get playlist: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if len(tracks) == 0 {
		http.Error(w, "playlist is empty", http.StatusBadRequest)
		return
	}

	trackIndex, positionMs := spotify.RadioTime(tracks, time.Now())

	slog.Info("debug/play",
		"playlist", s.cfg.SpotifyTestPlaylist,
		"device_id", deviceID,
		"track_index", trackIndex,
		"track_name", tracks[trackIndex].Name,
		"position_ms", positionMs,
	)

	if err := s.spotify.Play(ctx, deviceID, s.cfg.SpotifyTestPlaylist, trackIndex, positionMs); err != nil {
		slog.Error("debug/play: play failed", "err", err)
		http.Error(w, "play: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "device:    %s (%s)\nplaylist:  %s\ntrack[%d]: %s\nposition:  %dms\n",
		s.cfg.LibrespotDeviceName, deviceID,
		s.cfg.SpotifyTestPlaylist,
		trackIndex, tracks[trackIndex].Name,
		positionMs,
	)
}

// handleMusicConfig renders the music configuration page.
func (s *Server) handleMusicConfig(w http.ResponseWriter, r *http.Request) {
	type bucketRow struct {
		Index    int
		URI      string
		ImageURL string
	}
	rows := make([]bucketRow, s.cfg.BucketCount)
	uris := s.musicConfig.All()
	for i := 0; i < s.cfg.BucketCount; i++ {
		row := bucketRow{Index: i, URI: uris[i]}
		if uris[i] != "" {
			// Fetch playlist image asynchronously to avoid blocking if API is slow
			imageURL, err := s.spotify.GetPlaylistImage(r.Context(), uris[i])
			if err != nil {
				slog.Warn("failed to fetch music playlist image", "bucket", i, "uri", uris[i], "err", err)
			} else {
				row.ImageURL = imageURL
			}
		}
		rows[i] = row
	}
	s.render(w, "music", "base", map[string]any{
		"BucketCount": s.cfg.BucketCount,
		"Buckets":     rows,
	})
}

// handleMusicConfigSave processes form submission from the music config page.
func (s *Server) handleMusicConfigSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Extract bucket URIs from form: input names are like "bucket_0", "bucket_1", etc.
	for i := 0; i < s.cfg.BucketCount; i++ {
		key := fmt.Sprintf("bucket_%d", i)
		uri := r.FormValue(key)
		if err := s.musicConfig.Set(i, uri); err != nil {
			slog.Error("music config save failed", "bucket", i, "err", err)
		}
	}

	http.Redirect(w, r, "/config/music", http.StatusSeeOther)
}

// handlePodcastConfig renders the podcast configuration page.
func (s *Server) handlePodcastConfig(w http.ResponseWriter, r *http.Request) {
	type bucketRow struct {
		Index    int
		URI      string
		ImageURL string
	}
	rows := make([]bucketRow, s.cfg.BucketCount)
	uris := s.podcastConfig.All()
	for i := 0; i < s.cfg.BucketCount; i++ {
		row := bucketRow{Index: i, URI: uris[i]}
		if uris[i] != "" {
			// Fetch playlist image asynchronously to avoid blocking if API is slow
			imageURL, err := s.spotify.GetPlaylistImage(r.Context(), uris[i])
			if err != nil {
				slog.Warn("failed to fetch podcast playlist image", "bucket", i, "uri", uris[i], "err", err)
			} else {
				row.ImageURL = imageURL
			}
		}
		rows[i] = row
	}
	s.render(w, "podcast", "base", map[string]any{
		"BucketCount": s.cfg.BucketCount,
		"Buckets":     rows,
	})
}

// handlePodcastConfigSave processes form submission from the podcast config page.
func (s *Server) handlePodcastConfigSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Extract bucket URIs from form: input names are like "bucket_0", "bucket_1", etc.
	for i := 0; i < s.cfg.BucketCount; i++ {
		key := fmt.Sprintf("bucket_%d", i)
		uri := r.FormValue(key)
		if err := s.podcastConfig.Set(i, uri); err != nil {
			slog.Error("podcast config save failed", "bucket", i, "err", err)
		}
	}

	http.Redirect(w, r, "/config/podcast", http.StatusSeeOther)
}
