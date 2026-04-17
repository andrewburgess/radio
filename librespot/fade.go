package librespot

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const fadeSteps = 10

// FadeOut fades the librespot PipeWire sink input from 100% to 0% over
// duration. No-op if the sink cannot be found (librespot not yet playing, or
// pactl not available). On context cancellation the volume is snapped to 0 so
// the next switch starts from silence.
func (p *Process) FadeOut(ctx context.Context, duration time.Duration) {
	id, err := findSinkInputID()
	if err != nil {
		slog.Debug("librespot: fade out: sink not found", "err", err)
		return
	}
	p.sinkMu.Lock()
	p.sinkInputID = id
	p.sinkMu.Unlock()

	defer func() {
		if ctx.Err() != nil {
			_ = setSinkVolume(id, 0)
		}
	}()
	fadeSink(ctx, id, 100, 0, duration)
}

// FadeIn fades the librespot PipeWire sink input from 0% to 100% over
// duration. Uses the sink ID cached by the preceding FadeOut; if not cached
// (first play after startup) it waits up to 3 s for the sink to appear.
// Always snaps the sink to 0 before ramping so any audio that leaked through
// between Play() and this call is silenced first.
func (p *Process) FadeIn(ctx context.Context, duration time.Duration) {
	p.sinkMu.Lock()
	id := p.sinkInputID
	p.sinkMu.Unlock()

	if id < 0 {
		var err error
		id, err = waitForSinkInput(ctx, 3*time.Second)
		if err != nil {
			slog.Debug("librespot: fade in: sink not found", "err", err)
			return
		}
		p.sinkMu.Lock()
		p.sinkInputID = id
		p.sinkMu.Unlock()
	}

	_ = setSinkVolume(id, 0)
	fadeSink(ctx, id, 0, 100, duration)
}

func fadeSink(ctx context.Context, id, from, to int, duration time.Duration) {
	stepInterval := duration / fadeSteps
	for i := 0; i <= fadeSteps; i++ {
		pct := from + (to-from)*i/fadeSteps
		if err := setSinkVolume(id, pct); err != nil {
			slog.Debug("librespot: set sink volume", "id", id, "pct", pct, "err", err)
			return
		}
		if i < fadeSteps {
			select {
			case <-ctx.Done():
				return
			case <-time.After(stepInterval):
			}
		}
	}
}

func waitForSinkInput(ctx context.Context, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		if id, err := findSinkInputID(); err == nil {
			return id, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return -1, fmt.Errorf("timeout waiting for librespot sink input")
}

func findSinkInputID() (int, error) {
	out, err := exec.Command("pactl", "list", "sink-inputs").Output()
	if err != nil {
		return -1, fmt.Errorf("pactl: %w", err)
	}
	id, ok := parseSinkInputID(out)
	if !ok {
		return -1, fmt.Errorf("librespot sink input not found")
	}
	return id, nil
}

// parseSinkInputID scans `pactl list sink-inputs` output for a sink input
// whose properties include application.process.binary = "librespot".
func parseSinkInputID(data []byte) (int, bool) {
	currentID := -1
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(trimmed, "Sink Input #"); ok {
			if id, err := strconv.Atoi(after); err == nil {
				currentID = id
			}
			continue
		}
		if currentID >= 0 && strings.Contains(trimmed, `application.process.binary = "librespot"`) {
			return currentID, true
		}
	}
	return -1, false
}

func setSinkVolume(id, pct int) error {
	return exec.Command("pactl", "set-sink-input-volume",
		strconv.Itoa(id), strconv.Itoa(pct)+"%").Run()
}
