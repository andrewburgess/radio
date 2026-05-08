package server

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
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
	s.renderStationConfig(w, "music")
}

// handleMusicConfigSave processes form submission from the music config page.
func (s *Server) handleMusicConfigSave(w http.ResponseWriter, r *http.Request) {
	s.saveStationConfig(w, r, "music", "/config/music")
}

// handlePodcastConfig renders the podcast configuration page.
func (s *Server) handlePodcastConfig(w http.ResponseWriter, r *http.Request) {
	s.renderStationConfig(w, "podcast")
}

// handlePodcastConfigSave processes form submission from the podcast config page.
func (s *Server) handlePodcastConfigSave(w http.ResponseWriter, r *http.Request) {
	s.saveStationConfig(w, r, "podcast", "/config/podcast")
}

// handleAPIPlaylists returns an HTML fragment with the user's Spotify playlists.
// The offset query parameter controls pagination (default 0, page size 20).
// offset=0 renders the initial layout; offset>0 renders items+OOB load-more only.
func (s *Server) handleAPIPlaylists(w http.ResponseWriter, r *http.Request) {
	const pageSize = 50
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	playlists, total, err := s.spotify.GetUserPlaylists(r.Context(), offset, pageSize)
	if err != nil {
		slog.Error("api: get user playlists", "err", err)
		http.Error(w, "failed to fetch playlists", http.StatusInternalServerError)
		return
	}

	nextOffset := offset + pageSize
	tmplName := "playlist-initial"
	if offset > 0 {
		tmplName = "playlist-items"
	}

	s.render(w, "playlist-picker", tmplName, map[string]any{
		"Playlists":  playlists,
		"HasMore":    nextOffset < total,
		"NextOffset": nextOffset,
	})
}

// stationLabel returns a human-readable frequency label for a bucket.
// Music buckets map to FM (87.5-108.0 MHz); podcast buckets map to AM (530-1700 kHz).
func stationLabel(bucket, total int, mode string) string {
	if mode == "speaker" {
		return "AFC"
	}
	if total <= 1 {
		if mode == "music" {
			return "87.5 FM"
		}
		return "550 AM"
	}
	if mode == "music" {
		// North American FM: 87.9-107.9 MHz in 200 kHz steps (all odd tenths).
		// Interpolate bucket position over the 100 channel steps, then snap.
		const fmFirst = 87.9
		const fmSteps = 100 // 87.9 + 100x0.2 = 107.9
		channelIdx := int(math.Round(float64(bucket) * float64(fmSteps) / float64(total-1)))
		freq := fmFirst + float64(channelIdx)*0.2
		return fmt.Sprintf("%.1f FM", freq)
	}
	// AM: 550-1600 kHz, rounded to nearest 10 kHz (standard channel spacing)
	const amMin, amMax = 550, 1600
	freq := amMin + float64(bucket)*float64(amMax-amMin)/float64(total-1)
	rounded := int(math.Round(freq/10)) * 10
	return fmt.Sprintf("%d AM", rounded)
}

// renderStationConfig builds the bucket grid for music or podcast config pages.
func (s *Server) renderStationConfig(w http.ResponseWriter, mode string) {
	type bucketRow struct {
		Index    int
		Label    string
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
	var assignedURIs []string
	for i := 0; i < s.cfg.BucketCount; i++ {
		row := bucketRow{Index: i, Label: stationLabel(i, s.cfg.BucketCount, mode), URI: uriByBucket[i]}
		if row.URI != "" {
			// Render path: local cache only, no network calls.
			row.ImageURL = s.localImageURL(row.URI)
			assignedURIs = append(assignedURIs, row.URI)
		}
		rows[i] = row
	}

	s.render(w, mode, "base", map[string]any{
		"BucketCount": s.cfg.BucketCount,
		"Buckets":     rows,
	})

	// After the response is sent, check snapshots and download any missing or
	// stale images in the background so they're ready for the next page load.
	if len(assignedURIs) > 0 {
		go func() {
			for _, uri := range assignedURIs {
				s.cachedPlaylistImage(uri)
			}
		}()
	}
}

// saveStationConfig persists form-submitted bucket URIs for music or podcast mode.
func (s *Server) saveStationConfig(w http.ResponseWriter, r *http.Request, mode, redirectTo string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var assignedURIs []string
	for i := 0; i < s.cfg.BucketCount; i++ {
		uri := r.FormValue(fmt.Sprintf("bucket_%d", i))
		if err := s.store.SetStation(i, mode, uri, ""); err != nil {
			slog.Error("config: set station failed", "mode", mode, "bucket", i, "err", err)
		}
		if uri != "" {
			assignedURIs = append(assignedURIs, uri)
		}
	}

	http.Redirect(w, r, redirectTo, http.StatusSeeOther)

	if len(assignedURIs) > 0 {
		go func() {
			for _, uri := range assignedURIs {
				s.cacheStationLabel(uri, mode)
			}
		}()
	}
}

// handleMusicShuffle shuffles the playlist assigned to the given bucket by
// applying a random permutation via Spotify's reorder API. The playlist cache
// is automatically invalidated on next playback because the snapshot_id changes.
func (s *Server) handleMusicShuffle(w http.ResponseWriter, r *http.Request) {
	bucket, err := strconv.Atoi(r.PathValue("bucket"))
	if err != nil || bucket < 0 || bucket >= s.cfg.BucketCount {
		http.Error(w, "invalid bucket", http.StatusBadRequest)
		return
	}

	station, err := s.store.GetStation(bucket, "music")
	if err != nil {
		slog.Error("shuffle: get station", "bucket", bucket, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if station == nil || station.PlaylistURI == "" {
		http.Error(w, "no playlist assigned to this bucket", http.StatusBadRequest)
		return
	}

	if err := s.spotify.ShufflePlaylist(r.Context(), station.PlaylistURI); err != nil {
		slog.Error("shuffle: failed", "bucket", bucket, "uri", station.PlaylistURI, "err", err)
		http.Error(w, "shuffle failed", http.StatusInternalServerError)
		return
	}

	slog.Info("shuffle: complete", "bucket", bucket, "uri", station.PlaylistURI)
	w.WriteHeader(http.StatusNoContent)
}

// cacheStationLabel fetches the playlist name from Spotify and stores it as the
// station label so offline tools (e.g. gen-interstitial) can display it.
func (s *Server) cacheStationLabel(uri, mode string) {
	name, _, err := s.spotify.GetPlaylistInfo(context.Background(), uri)
	if err != nil {
		slog.Warn("config: fetch playlist name failed", "uri", uri, "err", err)
		return
	}
	stations, err := s.store.ListStations(mode)
	if err != nil {
		return
	}
	for _, st := range stations {
		if st.PlaylistURI == uri {
			if err := s.store.SetStation(st.Bucket, mode, uri, name); err != nil {
				slog.Warn("config: update station label failed", "uri", uri, "err", err)
			}
			return
		}
	}
}
