package server

import (
	"log/slog"
	"net/http"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"BucketCount": s.cfg.BucketCount,
	}
	if err := s.templates.ExecuteTemplate(w, "base", data); err != nil {
		slog.Error("template render failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
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
