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

// NotifyPlaying signals that librespot has started playing. It should be called
// whenever an EventPlaying is observed. Any FadeIn waiting via ArmFadeIn will
// unblock immediately.
func (p *Process) NotifyPlaying() {
	p.playingMu.Lock()
	ch := p.playingCh
	p.playingCh = make(chan struct{})
	p.playingMu.Unlock()
	close(ch)
}

// ArmFadeIn captures the current playing signal so that the subsequent FadeIn
// call will wait for the next EventPlaying before ramping volume up. Call this
// immediately before issuing the Spotify Play command so that a fast EventPlaying
// fired between Play returning and FadeIn being called is not missed.
func (p *Process) ArmFadeIn() {
	p.playingMu.Lock()
	ch := p.playingCh
	p.playingMu.Unlock()

	p.sinkMu.Lock()
	p.armedCh = ch
	p.sinkMu.Unlock()
}

// FadeOut fades the librespot PipeWire sink input from its current volume to 0%
// over duration. Uses currentVolPct as the starting point so it handles cases
// where the sink is already at a reduced level (e.g. ducked for an interstitial).
// Uses the cached sink ID when available, falling back to a pactl lookup.
// No-op if the sink cannot be found.
func (p *Process) FadeOut(ctx context.Context, duration time.Duration) {
	id := p.cachedSinkID()
	if id < 0 {
		var err error
		id, err = findSinkInputID()
		if err != nil {
			slog.Debug("librespot: fade out: sink not found", "err", err)
			return
		}
		p.sinkMu.Lock()
		p.sinkInputID = id
		p.sinkMu.Unlock()
	}

	p.sinkMu.Lock()
	fromPct := p.currentVolPct
	p.sinkMu.Unlock()

	if err := p.fadeSink(ctx, id, fromPct, 0, duration); err != nil {
		p.invalidateSinkID()
	}
}

// FadeIn waits for librespot to signal it has started playing (via the channel
// armed by ArmFadeIn), then fades the PipeWire sink input from 0% to 100%
// over duration. Times out after 3 s if no playing event arrives. Uses the
// cached sink ID; if not cached it waits up to 3 s for the sink to appear.
// Always snaps to 0 before ramping to silence any audio that leaked through.
func (p *Process) FadeIn(ctx context.Context, duration time.Duration) {
	p.sinkMu.Lock()
	armed := p.armedCh
	p.armedCh = nil
	p.sinkMu.Unlock()

	if armed != nil {
		select {
		case <-armed:
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
			slog.Debug("librespot: fade in: timeout waiting for playing event")
		}
	}

	id := p.cachedSinkID()
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

	_ = p.setSinkVolume(id, 0)
	if err := p.fadeSink(ctx, id, 0, 100, duration); err != nil {
		p.invalidateSinkID()
		// Cached sink ID was stale (new episode created a new sink input).
		// PipeWire's stream-restore may have applied the previous 0% volume to
		// the new sink, so find it and retry the fade on the correct ID.
		newID, findErr := waitForSinkInput(ctx, 3*time.Second)
		if findErr != nil {
			return
		}
		p.sinkMu.Lock()
		p.sinkInputID = newID
		p.sinkMu.Unlock()
		id = newID
		_ = p.setSinkVolume(id, 0)
		if err2 := p.fadeSink(ctx, id, 0, 100, duration); err2 != nil {
			p.invalidateSinkID()
			return
		}
	}
	// Safety net: guarantee full volume even if a step didn't land exactly at 100.
	if ctx.Err() == nil {
		_ = p.setSinkVolume(id, 100)
	}
}

// Duck fades the librespot PipeWire sink input from its current volume to
// targetPct over duration. Used to lower Spotify volume while an interstitial
// clip plays on top.
func (p *Process) Duck(ctx context.Context, targetPct int, duration time.Duration) {
	id := p.cachedSinkID()
	if id < 0 {
		var err error
		id, err = findSinkInputID()
		if err != nil {
			slog.Debug("librespot: duck: sink not found", "err", err)
			return
		}
		p.sinkMu.Lock()
		p.sinkInputID = id
		p.sinkMu.Unlock()
	}

	p.sinkMu.Lock()
	fromPct := p.currentVolPct
	p.sinkMu.Unlock()

	if err := p.fadeSink(ctx, id, fromPct, targetPct, duration); err != nil {
		p.invalidateSinkID()
	}
}

// Unduck fades the librespot PipeWire sink input from its current volume back
// to 100% over duration. Called after an interstitial clip finishes playing.
func (p *Process) Unduck(ctx context.Context, duration time.Duration) {
	id := p.cachedSinkID()
	if id < 0 {
		var err error
		id, err = findSinkInputID()
		if err != nil {
			slog.Debug("librespot: unduck: sink not found", "err", err)
			return
		}
		p.sinkMu.Lock()
		p.sinkInputID = id
		p.sinkMu.Unlock()
	}

	p.sinkMu.Lock()
	fromPct := p.currentVolPct
	p.sinkMu.Unlock()

	if err := p.fadeSink(ctx, id, fromPct, 100, duration); err != nil {
		p.invalidateSinkID()
	}
}

// SetVolumeDirect immediately sets the librespot PipeWire sink volume to pct
// (0–100) without any fade ramp. Used for live tune-quality ducking where low
// latency matters more than a smooth transition. No-op if the sink is not found.
func (p *Process) SetVolumeDirect(pct int) {
	id := p.cachedSinkID()
	if id < 0 {
		var err error
		id, err = findSinkInputID()
		if err != nil {
			slog.Debug("librespot: set volume direct: sink not found", "err", err)
			return
		}
		p.sinkMu.Lock()
		p.sinkInputID = id
		p.sinkMu.Unlock()
	}
	if err := p.setSinkVolume(id, pct); err != nil {
		slog.Debug("librespot: set volume direct", "id", id, "pct", pct, "err", err)
		p.invalidateSinkID()
	}
}

func (p *Process) cachedSinkID() int {
	p.sinkMu.Lock()
	defer p.sinkMu.Unlock()
	return p.sinkInputID
}

func (p *Process) invalidateSinkID() {
	p.sinkMu.Lock()
	p.sinkInputID = -1
	p.sinkMu.Unlock()
}

// fadeSink steps volume from `from` to `to` (both 0-100) in fadeSteps
// increments. Starts at step 1, not 0, so the first pactl call never resets
// the volume to `from` - this avoids an audible bump if a prior fade left the
// volume at a different level. Returns an error if a pactl call fails (stale
// sink ID); the caller should invalidate the cache in that case.
func (p *Process) fadeSink(ctx context.Context, id, from, to int, duration time.Duration) error {
	stepInterval := duration / fadeSteps
	for i := 1; i <= fadeSteps; i++ {
		pct := from + (to-from)*i/fadeSteps
		if err := p.setSinkVolume(id, pct); err != nil {
			slog.Debug("librespot: set sink volume", "id", id, "pct", pct, "err", err)
			return err
		}
		if i < fadeSteps {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(stepInterval):
			}
		}
	}
	return nil
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
		slog.Debug("librespot: pactl sink-inputs output", "output", string(out))
		return -1, fmt.Errorf("librespot sink input not found")
	}
	return id, nil
}

// parseSinkInputID scans `pactl list sink-inputs` output for the sink input
// belonging to librespot. Matches on application.name containing "librespot"
// (PipeWire/PulseAudio ALSA backend reports it as e.g.
// "ALSA plug-in [librespot-linux-arm64]").
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
		if currentID >= 0 &&
			strings.HasPrefix(trimmed, "application.name") &&
			strings.Contains(trimmed, "librespot") {
			return currentID, true
		}
	}
	return -1, false
}

// setSinkVolume calls pactl to set the PipeWire sink-input volume and records
// the new level in p.currentVolPct so subsequent fades start from the right value.
func (p *Process) setSinkVolume(id, pct int) error {
	err := exec.Command("pactl", "set-sink-input-volume",
		strconv.Itoa(id), strconv.Itoa(pct)+"%").Run()
	if err == nil {
		p.sinkMu.Lock()
		p.currentVolPct = pct
		p.sinkMu.Unlock()
	}
	return err
}
