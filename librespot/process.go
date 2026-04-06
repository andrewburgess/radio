package librespot

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"time"
)

// EventType identifies the kind of playback event emitted by librespot.
type EventType int

const (
	EventTrackChanged EventType = iota
	EventPlaying
	EventPaused
	EventStopped
)

// Event carries a parsed librespot playback event.
type Event struct {
	Type     EventType
	TrackURI string // populated for EventTrackChanged
}

// Config holds the parameters needed to launch librespot.
type Config struct {
	BinPath    string
	DeviceName string
	CacheDir   string
}

// Process manages the librespot subprocess lifecycle: it starts the binary,
// captures its output, restarts it on unexpected exit with exponential backoff,
// and emits parsed playback events on the Events channel.
type Process struct {
	cfg    Config
	Events chan Event

	mu      sync.Mutex
	cancel  context.CancelFunc
	stopCh  chan struct{}
	stopped bool
}

// New creates a Process. Call Start to launch the subprocess.
func New(cfg Config) *Process {
	return &Process{
		cfg:    cfg,
		Events: make(chan Event, 16),
		stopCh: make(chan struct{}),
	}
}

// Start launches the librespot subprocess in the background. It returns
// immediately; the subprocess is restarted on unexpected exit. Call Stop to
// shut down.
func (p *Process) Start() {
	go p.run()
}

// Stop signals librespot to shut down. It is safe to call more than once.
func (p *Process) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.stopped = true
	close(p.stopCh)
	if p.cancel != nil {
		p.cancel()
	}
}

// run is the supervisor loop: launches librespot and restarts it on unexpected
// exit using exponential backoff.
func (p *Process) run() {
	const minBackoff = time.Second
	const maxBackoff = 30 * time.Second
	backoff := minBackoff

	for {
		select {
		case <-p.stopCh:
			return
		default:
		}

		start := time.Now()
		if err := p.launch(); err != nil {
			slog.Error("librespot exited with error", "err", err)
		}

		select {
		case <-p.stopCh:
			return
		default:
		}

		// If the process ran for long enough, treat it as a successful run and
		// reset backoff so the next restart is immediate.
		if time.Since(start) > 10*time.Second {
			backoff = minBackoff
		}

		slog.Info("librespot restarting", "in", backoff)
		select {
		case <-p.stopCh:
			return
		case <-time.After(backoff):
			backoff = min(backoff*2, maxBackoff)
		}
	}
}

// launch starts one librespot process, wires up its output pipes, waits for
// it to exit, and returns any unexpected error. It returns nil if the process
// was stopped intentionally.
func (p *Process) launch() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()

	cmd := exec.CommandContext(ctx, p.cfg.BinPath,
		"--name", p.cfg.DeviceName,
		"--cache", p.cfg.CacheDir,
		"--disable-audio-cache",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("librespot: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("librespot: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("librespot: start: %w", err)
	}
	slog.Info("librespot started", "pid", cmd.Process.Pid, "bin", p.cfg.BinPath)

	// librespot (env_logger) writes all log output to stderr. We parse events
	// there and forward each line to the structured logger.
	// stdout is typically empty but we drain it to avoid blocking the process.
	//
	// TODO: Confirm the exact log line format for the deployed librespot version
	// and refine parseEvents. Consider --onevent for structured event delivery.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		p.parseEvents(stderr)
	}()
	go func() {
		defer wg.Done()
		drainReader(stdout)
	}()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			// Process was killed by Stop(); not an error.
			return nil
		}
		return fmt.Errorf("librespot: %w", err)
	}
	return nil
}

// parseEvents reads librespot's stderr line by line, emitting parsed playback
// events on p.Events and forwarding all lines to the structured logger.
func (p *Process) parseEvents(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		slog.Info("librespot", "msg", line)

		if evt, ok := parseLine(line); ok {
			select {
			case p.Events <- evt:
			default:
				slog.Warn("librespot event channel full, dropping event")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		slog.Error("librespot stderr read error", "err", err)
	}
}

// parseLine attempts to parse a librespot log line into a playback event.
// Returns the event and true if the line matched a known pattern.
//
// TODO: Fill in patterns once the exact log format is confirmed against the
// deployed librespot binary (check PLAN.md open questions).
func parseLine(line string) (Event, bool) {
	return Event{}, false
}

// drainReader discards all output from r.
func drainReader(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		slog.Debug("librespot stdout", "line", scanner.Text())
	}
}
