//go:build pi

package audio

import (
	"io"
	"log/slog"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
	mp3 "github.com/hajimehoshi/go-mp3"
)

// Static manages looping MP3 playback for no-signal buckets. A random file is
// chosen from the list when Start is called and played on repeat until Stop is
// called. The next call to Start picks a new random file.
//
// The oto audio context is created once on the first Start and reused for all
// subsequent Start/Stop cycles — oto only permits one context per process.
type Static struct {
	cfg    Config
	otoCtx *oto.Context // created once, reused across Start/Stop cycles

	mu      sync.Mutex
	playing bool
	stopCh  chan struct{}
}

func NewStatic(cfg Config) *Static {
	return &Static{cfg: cfg}
}

// Start begins playing the static audio. It is a no-op if already playing.
// A file is chosen randomly from cfg.Files at this point and looped until Stop.
func (s *Static) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.playing {
		return
	}

	file := s.pickFile()
	if file == "" {
		slog.Warn("static audio: no files configured")
		return
	}

	// Initialise the oto context once — subsequent calls reuse it.
	if s.otoCtx == nil {
		ctx, err := s.initOto(file)
		if err != nil {
			slog.Error("static audio: init oto", "err", err)
			return
		}
		s.otoCtx = ctx
	}

	stopCh := make(chan struct{})
	s.stopCh = stopCh
	s.playing = true

	go s.run(file, stopCh)
}

// Stop halts playback. Safe to call when not playing and safe to call more than once.
func (s *Static) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.playing {
		return
	}
	s.playing = false
	close(s.stopCh)
}

// IsPlaying reports whether static audio is currently active.
func (s *Static) IsPlaying() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.playing
}

// initOto opens file to read the sample rate, then creates the oto context.
func (s *Static) initOto(file string) (*oto.Context, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec, err := mp3.NewDecoder(f)
	if err != nil {
		return nil, err
	}

	ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
		SampleRate:   dec.SampleRate(),
		ChannelCount: 2,
		Format:       oto.FormatSignedInt16LE,
	})
	if err != nil {
		return nil, err
	}
	<-ready
	return ctx, nil
}

// pickFile returns a random file from cfg.Files, or "" if the list is empty.
// Must be called with s.mu held.
func (s *Static) pickFile() string {
	if len(s.cfg.Files) == 0 {
		return ""
	}
	return s.cfg.Files[rand.Intn(len(s.cfg.Files))]
}

// run opens file and loops playback until stopCh is closed.
func (s *Static) run(file string, stopCh <-chan struct{}) {
	f, err := os.Open(file)
	if err != nil {
		slog.Error("static audio: open file", "file", file, "err", err)
		s.mu.Lock()
		s.playing = false
		s.mu.Unlock()
		return
	}
	defer f.Close()

	dec, err := mp3.NewDecoder(f)
	if err != nil {
		slog.Error("static audio: decode MP3", "file", file, "err", err)
		s.mu.Lock()
		s.playing = false
		s.mu.Unlock()
		return
	}

	// Start at a random position so each session sounds different.
	seekRandom(dec)

	player := s.otoCtx.NewPlayer(dec)
	defer player.Close()
	player.Play()
	slog.Info("static audio started", "file", file)

	const checkInterval = 50 * time.Millisecond
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			slog.Info("static audio stopped")
			return
		case <-ticker.C:
			if !player.IsPlaying() {
				// EOF — rewind and keep looping the same file.
				if _, err := dec.Seek(0, io.SeekStart); err != nil {
					slog.Error("static audio: seek on loop", "err", err)
					return
				}
				player.Close()
				player = s.otoCtx.NewPlayer(dec)
				player.Play()
			}
		}
	}
}

// seekRandom seeks the decoder to a random position anywhere within the file
// so each session starts at a different point.
func seekRandom(dec *mp3.Decoder) {
	const bytesPerFrame = 4 // 2 channels × 2 bytes (int16)

	total := dec.Length()
	if total <= 0 {
		return
	}

	offset := rand.Int63n(total)
	offset -= offset % bytesPerFrame // align to frame boundary

	if _, err := dec.Seek(offset, io.SeekStart); err != nil {
		slog.Warn("static audio: random seek failed, starting from beginning", "err", err)
		dec.Seek(0, io.SeekStart)
	}
}
