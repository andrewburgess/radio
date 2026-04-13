package server

import (
	"fmt"
	"log/slog"
	"net/http"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if !s.spotify.Auth().HasToken() {
		s.render(w, "auth", "base", nil)
		return
	}
	s.render(w, "index", "base", s.state.snapshot(s.cfg.BucketCount))
}

// handleAuthLogout clears the stored Spotify token and redirects to /.
func (s *Server) handleAuthLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.spotify.Auth().Logout(); err != nil {
		slog.Error("logout failed", "err", err)
		http.Error(w, "logout failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleAuthStart begins the Spotify OAuth flow by redirecting the user to the
// Spotify authorization page.
func (s *Server) handleAuthStart(w http.ResponseWriter, r *http.Request) {
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

// handleMusicConfig renders the music configuration page.
func (s *Server) handleMusicConfig(w http.ResponseWriter, r *http.Request) {
	s.renderStationConfig(w, r, "music")
}

// handleMusicConfigSave processes form submission from the music config page.
func (s *Server) handleMusicConfigSave(w http.ResponseWriter, r *http.Request) {
	s.saveStationConfig(w, r, "music", "/config/music")
}

// handlePodcastConfig renders the podcast configuration page.
func (s *Server) handlePodcastConfig(w http.ResponseWriter, r *http.Request) {
	s.renderStationConfig(w, r, "podcast")
}

// handlePodcastConfigSave processes form submission from the podcast config page.
func (s *Server) handlePodcastConfigSave(w http.ResponseWriter, r *http.Request) {
	s.saveStationConfig(w, r, "podcast", "/config/podcast")
}

// renderStationConfig builds the bucket grid for music or podcast config pages.
func (s *Server) renderStationConfig(w http.ResponseWriter, r *http.Request, mode string) {
	type bucketRow struct {
		Index    int
		URI      string
		ImageURL string
	}

	stations, err := s.store.ListStations(mode)
	if err != nil {
		slog.Error("config: list stations failed", "mode", mode, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Build a lookup map so we can fill sparse results into a dense slice.
	uriByBucket := make(map[int]string, len(stations))
	for _, st := range stations {
		uriByBucket[st.Bucket] = st.PlaylistURI
	}

	rows := make([]bucketRow, s.cfg.BucketCount)
	for i := 0; i < s.cfg.BucketCount; i++ {
		row := bucketRow{Index: i, URI: uriByBucket[i]}
		if row.URI != "" {
			imageURL, err := s.spotify.GetPlaylistImage(r.Context(), row.URI)
			if err != nil {
				slog.Warn("config: fetch playlist image failed", "mode", mode, "bucket", i, "err", err)
			} else {
				row.ImageURL = imageURL
			}
		}
		rows[i] = row
	}

	s.render(w, mode, "base", map[string]any{
		"BucketCount": s.cfg.BucketCount,
		"Buckets":     rows,
	})
}

// saveStationConfig persists form-submitted bucket URIs for music or podcast mode.
func (s *Server) saveStationConfig(w http.ResponseWriter, r *http.Request, mode, redirectTo string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	for i := 0; i < s.cfg.BucketCount; i++ {
		uri := r.FormValue(fmt.Sprintf("bucket_%d", i))
		if err := s.store.SetStation(i, mode, uri, ""); err != nil {
			slog.Error("config: set station failed", "mode", mode, "bucket", i, "err", err)
		}
	}

	http.Redirect(w, r, redirectTo, http.StatusSeeOther)
}
