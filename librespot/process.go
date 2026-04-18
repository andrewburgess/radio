package librespot

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// EventType identifies the kind of playback event emitted by librespot.
type EventType int

const (
	EventTrackChanged EventType = iota
	EventPlaying
	EventPaused
	EventSeeked
	EventStopped
	EventEndOfTrack
	EventVolumeChanged
	EventSessionConnected
	EventSessionDisconnected
)

// ItemType distinguishes tracks from podcast episodes.
type ItemType string

const (
	ItemTypeTrack   ItemType = "Track"
	ItemTypeEpisode ItemType = "Episode"
)

// Event carries a parsed librespot playback event.
type Event struct {
	Type EventType

	// TrackChanged, Playing, Paused, Seeked, Stopped, EndOfTrack
	TrackID string

	// TrackChanged
	URI        string
	Name       string
	DurationMs int
	ItemType   ItemType
	Artists    string // comma-separated
	Album      string
	ShowName   string // podcast episodes

	// Playing, Paused, Seeked
	PositionMs int

	// VolumeChanged (0–65535)
	Volume int

	// SessionConnected, SessionDisconnected
	UserName     string
	ConnectionID string
}

// Config holds the parameters needed to launch librespot.
type Config struct {
	BinPath     string
	DeviceName  string
	DeviceType  string
	CacheDir    string
	AudioDevice string // ALSA device string, e.g. "plughw:CARD=Headphones,DEV=0"; empty = librespot default
}

// Process manages the librespot subprocess lifecycle.
// Start and Stop may be called multiple times to bring the process up and down
// with the radio power switch.
type Process struct {
	cfg    Config
	Events chan Event

	mu        sync.Mutex
	cancel    context.CancelFunc
	listener  net.Listener
	eventAddr string
	stopCh    chan struct{}
	running   bool

	sinkMu        sync.Mutex
	sinkInputID   int           // cached PipeWire sink-input index; -1 when unknown
	currentVolPct int           // last volume set via pactl (0-100); used as FadeOut start
	armedCh       chan struct{} // channel captured by ArmFadeIn, waited on by FadeIn

	playingMu sync.Mutex
	playingCh chan struct{} // closed each time EventPlaying fires, then replaced
}

// New creates a Process. Call Start to launch librespot.
func New(cfg Config) *Process {
	return &Process{
		cfg:           cfg,
		Events:        make(chan Event, 16),
		stopCh:        make(chan struct{}),
		sinkInputID:   -1,
		currentVolPct: 100,
		playingCh:     make(chan struct{}),
	}
}

// Start kills any leftover librespot process from a previous run, opens the
// event listener, then launches librespot in the background.
// It returns immediately; use Stop to shut down. Safe to call again after Stop.
func (p *Process) Start() error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return nil
	}
	p.stopCh = make(chan struct{})
	p.running = true
	p.mu.Unlock()

	p.killLeftover()
	if err := p.startListener(); err != nil {
		p.mu.Lock()
		p.running = false
		p.mu.Unlock()
		return fmt.Errorf("librespot: event socket: %w", err)
	}
	go p.run()
	return nil
}

// pidFilePath returns the path used to record the running librespot PID.
func (p *Process) pidFilePath() string {
	return filepath.Join(p.cfg.CacheDir, "librespot.pid")
}

// killLeftover reads the pidfile from a previous run and kills that process if
// it is still running. Errors are logged but not returned — a stale or missing
// pidfile is not fatal.
func (p *Process) killLeftover() {
	path := p.pidFilePath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		slog.Warn("librespot: read pidfile", "err", err)
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		slog.Warn("librespot: parse pidfile", "err", err)
		os.Remove(path)
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		// On Windows FindProcess fails if the process doesn't exist.
		os.Remove(path)
		return
	}
	if err := proc.Kill(); err != nil {
		slog.Warn("librespot: kill leftover process", "pid", pid, "err", err)
	} else {
		slog.Info("librespot: killed leftover process", "pid", pid)
	}
	os.Remove(path)
}

// writePidFile records pid so a future startup can clean it up if needed.
func (p *Process) writePidFile(pid int) {
	path := p.pidFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		slog.Warn("librespot: create cache dir for pidfile", "err", err)
		return
	}
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)), 0644); err != nil {
		slog.Warn("librespot: write pidfile", "err", err)
	}
}

// removePidFile deletes the pidfile when librespot exits cleanly.
func (p *Process) removePidFile() {
	os.Remove(p.pidFilePath())
}

// Stop signals librespot to shut down and closes the event socket.
// Safe to call more than once. After Stop, Start may be called again.
func (p *Process) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return
	}
	p.running = false
	close(p.stopCh)
	if p.cancel != nil {
		p.cancel()
	}
	if p.listener != nil {
		p.listener.Close()
		p.listener = nil
	}
	p.sinkMu.Lock()
	p.sinkInputID = -1
	p.currentVolPct = 100
	p.sinkMu.Unlock()
	p.playingMu.Lock()
	p.playingCh = make(chan struct{})
	p.playingMu.Unlock()
}

// startListener opens a TCP loopback listener that event forwarder subprocesses
// connect to for each librespot event. The OS assigns an ephemeral port.
func (p *Process) startListener() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.listener = ln
	p.eventAddr = ln.Addr().String()
	p.mu.Unlock()

	go p.acceptEvents(ln)
	return nil
}

// acceptEvents is the event listener accept loop; runs until Stop closes the
// listener.
func (p *Process) acceptEvents(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-p.stopCh:
				return
			default:
				slog.Error("librespot: event socket accept error", "err", err)
				return
			}
		}
		go p.handleEventConn(conn)
	}
}

// handleEventConn reads one JSON-encoded event from conn and emits it on
// p.Events.
func (p *Process) handleEventConn(conn net.Conn) {
	defer conn.Close()

	var raw map[string]string
	if err := json.NewDecoder(conn).Decode(&raw); err != nil {
		slog.Error("librespot: event decode error", "err", err)
		return
	}

	evt, ok := parseRawEvent(raw)
	if !ok {
		slog.Debug("librespot: unhandled event", "player_event", raw["PLAYER_EVENT"])
		return
	}

	select {
	case p.Events <- evt:
	default:
		slog.Warn("librespot: event channel full, dropping event", "type", raw["PLAYER_EVENT"])
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

// launch starts one librespot process and waits for it to exit. Returns nil
// if the process was stopped intentionally via Stop.
func (p *Process) launch() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p.mu.Lock()
	p.cancel = cancel
	p.mu.Unlock()

	selfExe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("librespot: resolve executable path: %w", err)
	}

	args := []string{
		"--name", p.cfg.DeviceName,
		"--device-type", p.cfg.DeviceType,
		"--cache", p.cfg.CacheDir,
		"--enable-oauth",
		"--onevent", selfExe,
	}
	if p.cfg.AudioDevice != "" {
		args = append(args, "--device", p.cfg.AudioDevice)
	}
	cmd := exec.CommandContext(ctx, p.cfg.BinPath, args...)

	// Inherit the current environment and add the TCP address so that event
	// forwarder subprocesses know where to connect.
	cmd.Env = append(os.Environ(), "RADIO_EVENT_ADDR="+p.eventAddr)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("librespot: stderr pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("librespot: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("librespot: start: %w", err)
	}
	slog.Info("librespot started", "pid", cmd.Process.Pid, "bin", p.cfg.BinPath)
	p.writePidFile(cmd.Process.Pid)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		logLines(stderr)
	}()
	go func() {
		defer wg.Done()
		drainReader(stdout)
	}()
	wg.Wait()

	if err := cmd.Wait(); err != nil {
		p.removePidFile()
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("librespot: %w", err)
	}
	p.removePidFile()
	return nil
}

// logLines forwards lines from r to the structured logger at Info level.
func logLines(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		slog.Debug("librespot", "msg", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		slog.Error("librespot stderr read error", "err", err)
	}
}

// drainReader discards all output from r.
func drainReader(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		slog.Debug("librespot stdout", "line", scanner.Text())
	}
}

// parseRawEvent converts the env var map sent by the forwarder into a typed
// Event. Returns false for event types we don't act on.
func parseRawEvent(raw map[string]string) (Event, bool) {
	evt := Event{
		TrackID:      raw["TRACK_ID"],
		URI:          raw["URI"],
		Name:         raw["NAME"],
		ItemType:     ItemType(raw["ITEM_TYPE"]),
		Artists:      raw["ARTISTS"],
		Album:        raw["ALBUM"],
		ShowName:     raw["SHOW_NAME"],
		UserName:     raw["USER_NAME"],
		ConnectionID: raw["CONNECTION_ID"],
		DurationMs:   parseInt(raw["DURATION_MS"]),
		PositionMs:   parseInt(raw["POSITION_MS"]),
		Volume:       parseInt(raw["VOLUME"]),
	}

	switch raw["PLAYER_EVENT"] {
	case "track_changed":
		evt.Type = EventTrackChanged
	case "playing":
		evt.Type = EventPlaying
	case "paused":
		evt.Type = EventPaused
	case "seeked":
		evt.Type = EventSeeked
	case "stopped":
		evt.Type = EventStopped
	case "end_of_track":
		evt.Type = EventEndOfTrack
	case "volume_changed":
		evt.Type = EventVolumeChanged
	case "session_connected":
		evt.Type = EventSessionConnected
	case "session_disconnected":
		evt.Type = EventSessionDisconnected
	default:
		return Event{}, false
	}

	return evt, true
}

func parseInt(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// RunEventForwarder is called when the binary is invoked by librespot as the
// --onevent handler. It reads the event env vars librespot set, connects to
// the event socket, and writes a JSON-encoded map of the vars.
//
// Detection: librespot always sets PLAYER_EVENT before invoking the handler,
// so main calls this when that variable is present.
func RunEventForwarder() {
	addr := os.Getenv("RADIO_EVENT_ADDR")
	if addr == "" {
		fmt.Fprintln(os.Stderr, "radio event forwarder: RADIO_EVENT_ADDR not set")
		os.Exit(1)
	}

	keys := []string{
		"PLAYER_EVENT",
		"TRACK_ID", "URI", "NAME", "DURATION_MS", "POSITION_MS",
		"ITEM_TYPE", "ARTISTS", "ALBUM", "ALBUM_ARTISTS",
		"SHOW_NAME", "PUBLISH_TIME",
		"NUMBER", "DISC_NUMBER",
		"VOLUME",
		"SHUFFLE", "REPEAT", "AUTO_PLAY",
		"USER_NAME", "CONNECTION_ID",
		"CLIENT_ID", "CLIENT_NAME",
	}

	payload := make(map[string]string, len(keys))
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			payload[k] = v
		}
	}

	data, err := json.Marshal(payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "radio event forwarder: marshal: %v\n", err)
		os.Exit(1)
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "radio event forwarder: dial: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if _, err := conn.Write(data); err != nil {
		fmt.Fprintf(os.Stderr, "radio event forwarder: write: %v\n", err)
		os.Exit(1)
	}
}
