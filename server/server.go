package server

import (
	"context"
	"embed"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"andrewburgess.io/radio/audio"
	"andrewburgess.io/radio/config"
	"andrewburgess.io/radio/events"
	"andrewburgess.io/radio/spotify"
	"andrewburgess.io/radio/store"
)

//go:embed templates/*
var templateFS embed.FS

// AmpController abstracts the amplifier SD pin so the server doesn't depend
// on the hardware package directly.
type AmpController interface {
	Mute()
	Unmute()
}

// LibrespotController abstracts the librespot process so the station controller
// can start and stop it with the power switch.
type LibrespotController interface {
	Start() error
	Stop()
	FadeOut(ctx context.Context, duration time.Duration)
	ArmFadeIn()
	FadeIn(ctx context.Context, duration time.Duration)
	Duck(ctx context.Context, targetPct int, duration time.Duration)
	Unduck(ctx context.Context, duration time.Duration)
}

type Server struct {
	cfg                   *config.Config
	staticMinGain         float64
	interstitialDuckLevel int
	mux                   *http.ServeMux
	httpServer            *http.Server
	templates             map[string]*template.Template
	spotify               *spotify.Client
	store                 *store.Store
	bus                   *events.Bus
	staticAudio           *audio.Static
	interstitials         *audio.InterstitialPlayer
	amp                   AmpController
	librespot             LibrespotController
	state                 *radioState
	broker                *sseBroker
	imageDir              string
}

func New(cfg *config.Config, spotifyClient *spotify.Client, db *store.Store, bus *events.Bus, staticAudio *audio.Static, amp AmpController, librespot LibrespotController, interstitials *audio.InterstitialPlayer) (*Server, error) {
	funcMap := template.FuncMap{
		"showDebug": func() bool { return cfg.ShowDebug },
	}

	pages := []string{"index", "auth", "debug", "music", "podcast"}
	templates := make(map[string]*template.Template, len(pages)+1)
	for _, page := range pages {
		tmpl, err := template.New("").Funcs(funcMap).ParseFS(templateFS,
			"templates/base.html",
			"templates/"+page+".html",
		)
		if err != nil {
			return nil, err
		}
		templates[page] = tmpl
	}
	// Fragment templates rendered without the base layout.
	for _, frag := range []string{"playlist-picker"} {
		tmpl, err := template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/"+frag+".html")
		if err != nil {
			return nil, err
		}
		templates[frag] = tmpl
	}

	s := &Server{
		cfg:                   cfg,
		staticMinGain:         cfg.DialStaticMinGain,
		interstitialDuckLevel: cfg.InterstitialDuckLevel,
		mux:                   http.NewServeMux(),
		templates:             templates,
		spotify:               spotifyClient,
		store:                 db,
		bus:                   bus,
		staticAudio:           staticAudio,
		interstitials:         interstitials,
		amp:                   amp,
		librespot:             librespot,
		state:                 newRadioState(),
		broker:                newSSEBroker(),
		imageDir:              cfg.ImageCacheDir,
	}

	go s.runStateUpdater()
	go s.runSSEPublisher()
	go s.runStationController()
	s.registerRoutes()
	return s, nil
}

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("GET /", s.handleIndex)
	s.mux.HandleFunc("GET /auth", s.handleAuthStart)
	s.mux.HandleFunc("POST /auth/logout", s.handleAuthLogout)
	s.mux.HandleFunc("GET /auth/callback", s.handleAuthCallback)
	s.mux.HandleFunc("GET /config/music", s.handleMusicConfig)
	s.mux.HandleFunc("POST /config/music", s.handleMusicConfigSave)
	s.mux.HandleFunc("GET /config/podcast", s.handlePodcastConfig)
	s.mux.HandleFunc("POST /config/podcast", s.handlePodcastConfigSave)
	s.mux.HandleFunc("GET /api/playlists", s.handleAPIPlaylists)
	s.mux.HandleFunc("GET /images/{filename}", s.handleImages)
	s.mux.HandleFunc("GET /debug", s.handleDebug)
	s.mux.HandleFunc("GET /debug/state", s.handleDebugState)
	s.mux.HandleFunc("POST /debug/simulate", s.handleDebugSimulate)
	s.mux.HandleFunc("POST /debug/interstitial", s.handleDebugInterstitial)
	s.mux.HandleFunc("GET /events", s.handleSSE)
}

// requireAuth is middleware that redirects unauthenticated requests to /auth.
// Requests to /auth and /auth/callback are always passed through.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/auth" || r.URL.Path == "/auth/callback" {
			next.ServeHTTP(w, r)
			return
		}
		if !s.spotify.Auth().HasToken() {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
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

// Start begins serving HTTP requests. It blocks until the server exits.
// Returns http.ErrServerClosed on clean shutdown.
func (s *Server) Start() error {
	addr := ":" + s.cfg.Port
	slog.Info("server starting", "addr", addr)
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.requireAuth(s.mux),
	}
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully drains in-flight requests, waiting up to ctx's deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}
