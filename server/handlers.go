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
