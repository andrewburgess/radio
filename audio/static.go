package audio

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os/exec"
	"runtime"
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

	bin, args := s.buildCommand()
	cmd := exec.CommandContext(ctx, bin, args...)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("static audio: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("static audio: start: %w", err)
	}
	slog.Info("static audio started", "pid", cmd.Process.Pid, "bin", bin, "args", args)

	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			slog.Warn("static audio stderr", "line", scanner.Text())
		}
	}()

	err = cmd.Wait()
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

// randomSeekOffset returns a random seek position in seconds within
// [0, maxOffsetSecs). Used to avoid always starting static audio at the
// beginning of the file on each launch.
const maxOffsetSecs = 120

func randomSeekOffset() string {
	return fmt.Sprintf("%d", rand.Intn(maxOffsetSecs))
}

// buildCommand returns the binary and arguments to launch for the configured
// player. The binary may differ from s.cfg.Bin when a platform fallback is used.
// A random -ss seek offset is applied for ffmpeg/ffplay so each launch starts
// at a different point in the file.
//
//   - ffmpeg + ALSA sink:  ffmpeg -loglevel error -ss <offset> -stream_loop -1 -i <file> -f alsa <sink>
//   - ffmpeg + macOS:      ffplay -nodisp -ss <offset> -loop 0 <file>
//   - ffplay:              ffplay -nodisp -ss <offset> -loop 0 <file>
//   - aplay:               aplay -q <file>  (no seek support; supervisor loop provides looping)
//   - afplay:              afplay <file>    (no seek support; supervisor loop provides looping)
func (s *Static) buildCommand() (string, []string) {
	bin := s.cfg.Bin
	// Normalise to just the binary name for matching (handles full paths).
	for i := len(bin) - 1; i >= 0; i-- {
		if bin[i] == '/' || bin[i] == '\\' {
			bin = bin[i+1:]
			break
		}
	}

	ss := randomSeekOffset()

	switch bin {
	case "aplay":
		return s.cfg.Bin, []string{"-q", s.cfg.File}
	case "afplay":
		return s.cfg.Bin, []string{s.cfg.File}
	case "ffplay":
		return s.cfg.Bin, []string{"-nodisp", "-loglevel", "error", "-ss", ss, "-loop", "0", s.cfg.File}
	default: // ffmpeg
		if s.cfg.Sink != "" {
			return s.cfg.Bin, []string{
				"-loglevel", "error",
				"-ss", ss,
				"-stream_loop", "-1",
				"-i", s.cfg.File,
				"-f", "alsa", s.cfg.Sink,
			}
		}
		if runtime.GOOS == "darwin" {
			// ffmpeg has no reliable macOS audio muxer; use ffplay which ships with it.
			return "ffplay", []string{"-nodisp", "-loglevel", "error", "-ss", ss, "-loop", "0", s.cfg.File}
		}
		// Linux without a sink: return ffmpeg args as-is; caller must set STATIC_AUDIO_SINK.
		return s.cfg.Bin, []string{
			"-loglevel", "error",
			"-ss", ss,
			"-stream_loop", "-1",
			"-i", s.cfg.File,
		}
	}
}
