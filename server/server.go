package server

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"

	"andrewburgess.io/radio/config"
)

//go:embed templates/*
var templateFS embed.FS

type Server struct {
	cfg       *config.Config
	mux       *http.ServeMux
	templates *template.Template
}

func New(cfg *config.Config) (*Server, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}

	s := &Server{
		cfg:       cfg,
		mux:       http.NewServeMux(),
		templates: tmpl,
	}

	s.registerRoutes()
	return s, nil
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /", s.handleIndex)
}

func (s *Server) Start() error {
	addr := ":" + s.cfg.Port
	slog.Info("server starting", "addr", addr)
	return http.ListenAndServe(addr, s.mux)
}
