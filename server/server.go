package server

import (
	"embed"
	"html/template"
	"log/slog"
	"net/http"

	"andrewburgess.io/radio/config"
	"andrewburgess.io/radio/events"
	"andrewburgess.io/radio/spotify"
)

//go:embed templates/*
var templateFS embed.FS

type Server struct {
	cfg           *config.Config
	mux           *http.ServeMux
	templates     map[string]*template.Template
	spotify       *spotify.Client
	playlistCache spotify.PlaylistCacheStore
	bus           *events.Bus
	state         *radioState
	broker        *sseBroker
}

func New(cfg *config.Config, spotifyClient *spotify.Client, playlistCache spotify.PlaylistCacheStore, bus *events.Bus) (*Server, error) {
	pages := []string{"index", "debug"}
	templates := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		tmpl, err := template.ParseFS(templateFS,
			"templates/base.html",
			"templates/"+page+".html",
		)
		if err != nil {
			return nil, err
		}
		templates[page] = tmpl
	}

	s := &Server{
		cfg:           cfg,
		mux:           http.NewServeMux(),
		templates:     templates,
		spotify:       spotifyClient,
		playlistCache: playlistCache,
		bus:           bus,
		state:         newRadioState(),
		broker:        newSSEBroker(),
	}

	go s.runStateUpdater()
	go s.runSSEPublisher()
	s.registerRoutes()
	return s, nil
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("GET /auth", s.handleAuth)
	s.mux.HandleFunc("GET /auth/callback", s.handleAuthCallback)
	s.mux.HandleFunc("GET /debug", s.handleDebug)
	s.mux.HandleFunc("GET /debug/state", s.handleDebugState)
	s.mux.HandleFunc("POST /debug/simulate", s.handleDebugSimulate)
	s.mux.HandleFunc("GET /debug/play", s.handleDebugPlay)
	s.mux.HandleFunc("GET /debug/cache", s.handleDebugCache)
	s.mux.HandleFunc("GET /events", s.handleSSE)
}

// render executes the named template from the named page's template set.
func (s *Server) render(w http.ResponseWriter, page, tmpl string, data any) {
	t, ok := s.templates[page]
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := t.ExecuteTemplate(w, tmpl, data); err != nil {
		slog.Error("render failed", "page", page, "tmpl", tmpl, "err", err)
	}
}

// runStateUpdater subscribes to the event bus and keeps s.state current.
func (s *Server) runStateUpdater() {
	ch := s.bus.Subscribe()
	for e := range ch {
		s.state.update(e)
	}
}

func (s *Server) Start() error {
	addr := ":" + s.cfg.Port
	slog.Info("server starting", "addr", addr)
	return http.ListenAndServe(addr, s.mux)
}
