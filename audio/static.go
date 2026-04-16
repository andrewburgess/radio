//go:build pi

package audio

import (
	"io"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"sync"
	"time"

	"github.com/ebitengine/oto/v3"
	mp3 "github.com/hajimehoshi/go-mp3"
)

// gainReader wraps an io.Reader and applies a smoothly-ramped gain (0–1) to
// each int16 PCM sample. Gain ramps toward the target at a fixed per-sample
// rate to avoid audible clicks on sudden changes.
type gainReader struct {
	r       io.Reader
	mu      sync.Mutex
	current float32
	target  float32
}

// rampPerSample gives a full 0→1 swing in ~200 ms at 48 kHz.
const rampPerSample = float32(1.0 / 9600.0)

func newGainReader(r io.Reader, initial float32) *gainReader {
	return &gainReader{r: r, current: initial, target: initial}
}

func (g *gainReader) setTarget(v float32) {
	g.mu.Lock()
	g.target = v
	g.mu.Unlock()
}

func (g *gainReader) Read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	if n == 0 {
		return n, err
	}

	g.mu.Lock()
	cur := g.current
	tgt := g.target
	g.mu.Unlock()

	for i := 0; i+1 < n; i += 2 {
		if cur < tgt {
			cur += rampPerSample
			if cur > tgt {
				cur = tgt
			}
		} else if cur > tgt {
			cur -= rampPerSample
			if cur < tgt {
				cur = tgt
			}
		}

		sample := int16(p[i]) | int16(p[i+1])<<8
		scaled := float32(sample) * cur
		if scaled > 32767 {
			scaled = 32767
		} else if scaled < -32768 {
			scaled = -32768
		}
		out := int16(scaled)
		p[i] = byte(out)
		p[i+1] = byte(out >> 8)
	}

	g.mu.Lock()
	g.current = cur
	g.mu.Unlock()

	return n, err
}

// Static manages looping MP3 playback for the static noise layer. It runs
// continuously while the radio is powered on; volume is controlled via
// SetGain (0=silent, 1=full). Call Shuffle to immediately jump to a new
// random file and position — used when the dial lands in a station sweet spot.
//
// The oto audio context is created once on first Start and reused across
// Stop/Start cycles — oto only permits one context per process.
type Static struct {
	cfg    Config
	otoCtx *oto.Context

	mu         sync.Mutex
	playing    bool
	stopCh     chan struct{}
	shuffleCh  chan struct{}
	gainTarget float32     // persists across file changes; default 0 (silent)
	gr         *gainReader // active gain reader, nil when stopped
}

func NewStatic(cfg Config) *Static {
	return &Static{
		cfg:       cfg,
		shuffleCh: make(chan struct{}, 1),
	}
}

// Start begins playing static audio at the current gain level.
// It is a no-op if already playing.
func (s *Static) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.playing {
		return
	}

	if s.otoCtx == nil {
		file := s.pickFile()
		if file == "" {
			slog.Warn("static audio: no files configured")
			return
		}
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

	go s.run(stopCh)
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

// SetGain sets the target playback gain (0.0 = silent, 1.0 = full volume).
// The change is applied smoothly over ~200 ms to avoid clicks. Safe to call
// from any goroutine at any time, including when not playing.
func (s *Static) SetGain(g float64) {
	v := float32(math.Max(0, math.Min(1, g)))
	s.mu.Lock()
	s.gainTarget = v
	gr := s.gr
	s.mu.Unlock()
	if gr != nil {
		gr.setTarget(v)
	}
}

// Shuffle signals the player to immediately jump to a new random file and
// position. Used when the dial enters a station sweet spot. Non-blocking;
// if a shuffle is already pending it is coalesced.
func (s *Static) Shuffle() {
	select {
	case s.shuffleCh <- struct{}{}:
	default:
	}
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
// cfg.Files is immutable after construction; no lock needed.
func (s *Static) pickFile() string {
	if len(s.cfg.Files) == 0 {
		return ""
	}
	return s.cfg.Files[rand.Intn(len(s.cfg.Files))]
}

// run is the supervisor loop: picks a file and calls runFile. If runFile
// returns true (shuffle requested) it loops immediately with a new file.
func (s *Static) run(stopCh <-chan struct{}) {
	for {
		select {
		case <-stopCh:
			return
		default:
		}

		file := s.pickFile()
		if file == "" {
			return
		}

		if reshuffled := s.runFile(file, stopCh); !reshuffled {
			return
		}
	}
}

// runFile plays file until stopCh fires (returns false) or a shuffle is
// requested (returns true — supervisor will restart with a new file).
func (s *Static) runFile(file string, stopCh <-chan struct{}) (reshuffled bool) {
	f, err := os.Open(file)
	if err != nil {
		slog.Error("static audio: open file", "file", file, "err", err)
		return false
	}
	defer f.Close()

	dec, err := mp3.NewDecoder(f)
	if err != nil {
		slog.Error("static audio: decode MP3", "file", file, "err", err)
		return false
	}
	seekRandom(dec)

	s.mu.Lock()
	gr := newGainReader(dec, s.gainTarget)
	s.gr = gr
	s.mu.Unlock()

	player := s.otoCtx.NewPlayer(gr)
	defer func() {
		player.Close()
		s.mu.Lock()
		if s.gr == gr {
			s.gr = nil
		}
		s.mu.Unlock()
	}()
	player.Play()
	slog.Info("static audio: playing", "file", file)

	const checkInterval = 50 * time.Millisecond
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			slog.Info("static audio: stopped")
			return false
		case <-s.shuffleCh:
			slog.Debug("static audio: reshuffling")
			return true
		case <-ticker.C:
			if !player.IsPlaying() {
				// EOF — rewind and loop the same file.
				if _, err := dec.Seek(0, io.SeekStart); err != nil {
					slog.Error("static audio: seek on loop", "err", err)
					return false
				}
				player.Close()
				player = s.otoCtx.NewPlayer(gr)
				player.Play()
			}
		}
	}
}

// seekRandom seeks the decoder to a random position within the file so each
// session starts at a different point.
func seekRandom(dec *mp3.Decoder) {
	const bytesPerFrame = 4 // 2 channels × 2 bytes (int16)

	total := dec.Length()
	if total <= 0 {
		return
	}

	offset := rand.Int63n(total)
	offset -= offset % bytesPerFrame

	if _, err := dec.Seek(offset, io.SeekStart); err != nil {
		slog.Warn("static audio: random seek failed, starting from beginning", "err", err)
		dec.Seek(0, io.SeekStart) //nolint:errcheck
	}
}
