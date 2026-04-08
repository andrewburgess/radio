package audio

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// Config holds the parameters for the static audio subprocess.
type Config struct {
	// Bin is the path to the audio player binary. Defaults to "ffmpeg".
	Bin string

	// File is the path to the audio file to loop. Defaults to "static/noise.mp3".
	File string

	// Sink is the ALSA output device (e.g. "hw:0"). When empty, the player
	// selects its default output — suitable for macOS dev with ffmpeg.
	Sink string
}

// Static manages a looping audio subprocess for no-signal buckets. It plays
// a configured audio file on repeat until Stop is called.
type Static struct {
	cfg Config

	mu      sync.Mutex
	cancel  context.CancelFunc
	playing bool
	stopCh  chan struct{}
}

func NewStatic(cfg Config) *Static {
	return &Static{cfg: cfg}
}

// Start begins playing the static audio. It is a no-op if already playing.
func (s *Static) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.playing {
		return
	}

	stopCh := make(chan struct{})
	s.stopCh = stopCh
	s.playing = true

	go s.run(stopCh)
}

// Stop halts the static audio subprocess. It is safe to call when not playing
// and safe to call more than once.
func (s *Static) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.playing {
		return
	}
	s.playing = false
	if s.cancel != nil {
		s.cancel()
	}
	close(s.stopCh)
}

// IsPlaying reports whether static audio is currently active.
func (s *Static) IsPlaying() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.playing
}

// run is the supervisor loop: launches the audio player and restarts it on
// unexpected exit. It returns when stopCh is closed.
func (s *Static) run(stopCh <-chan struct{}) {
	const minBackoff = 500 * time.Millisecond
	const maxBackoff = 10 * time.Second
	backoff := minBackoff

	for {
		select {
		case <-stopCh:
			return
		default:
		}

		start := time.Now()
		if err := s.launch(stopCh); err != nil {
			slog.Error("static audio exited with error", "err", err)
		}

		select {
		case <-stopCh:
			return
		default:
		}

		if time.Since(start) > 5*time.Second {
			backoff = minBackoff
		}

		select {
		case <-stopCh:
			return
		case <-time.After(backoff):
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// launch starts one instance of the audio player and waits for it to exit.
// Returns nil if the process was stopped intentionally via Stop.
func (s *Static) launch(stopCh <-chan struct{}) error {
	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	defer cancel()

	cmd := exec.CommandContext(ctx, s.cfg.Bin, s.buildArgs()...)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("static audio: start: %w", err)
	}
	slog.Info("static audio started", "pid", cmd.Process.Pid, "file", s.cfg.File)

	err := cmd.Wait()
	if ctx.Err() != nil {
		return nil // stopped intentionally
	}
	select {
	case <-stopCh:
		return nil
	default:
	}
	return err
}

// buildArgs constructs the command-line arguments for the configured player.
//
// ffmpeg (default): loops the file indefinitely.
//   - With sink:    ffmpeg -loglevel quiet -stream_loop -1 -i <file> -f alsa <sink>
//   - Without sink: ffmpeg -loglevel quiet -stream_loop -1 -i <file> -f ...auto
//
// aplay: plays the file once; the supervisor loop in run() provides the loop
// by restarting the process on exit.
//   - aplay -q <file>
func (s *Static) buildArgs() []string {
	bin := s.cfg.Bin
	// Normalise to just the binary name for matching (handles full paths).
	for i := len(bin) - 1; i >= 0; i-- {
		if bin[i] == '/' || bin[i] == '\\' {
			bin = bin[i+1:]
			break
		}
	}

	switch bin {
	case "aplay":
		return []string{"-q", s.cfg.File}
	default: // ffmpeg
		args := []string{
			"-loglevel", "quiet",
			"-stream_loop", "-1",
			"-i", s.cfg.File,
		}
		if s.cfg.Sink != "" {
			args = append(args, "-f", "alsa", s.cfg.Sink)
		}
		return args
	}
}
