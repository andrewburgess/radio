package server

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"

	"andrewburgess.io/radio/config"
	"andrewburgess.io/radio/spotify"
)

//go:embed templates/*
var templateFS embed.FS

type Server struct {
	cfg       *config.Config
	mux       *http.ServeMux
	templates *template.Template
	spotify   *spotify.Client
}

func New(cfg *config.Config, spotifyClient *spotify.Client) (*Server, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:       cfg,
		mux:       http.NewServeMux(),
		templates: tmpl,
		spotify:   spotifyClient,
	}

	s.registerRoutes()
	return s, nil
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("GET /auth", s.handleAuth)
	s.mux.HandleFunc("GET /auth/callback", s.handleAuthCallback)
	s.mux.HandleFunc("GET /debug/play", s.handleDebugPlay)
}

func (s *Server) Start() error {
	addr := ":" + s.cfg.Port
	slog.Info("server starting", "addr", addr)
	return http.ListenAndServe(addr, s.mux)
}
