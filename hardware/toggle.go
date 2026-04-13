//go:build pi

package hardware

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"andrewburgess.io/radio/events"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

const togglePollInterval = 100 * time.Millisecond

// Toggle reads the AM/AFC/FM 3-position switch using two GPIO pins and
// publishes KindToggleSwitched events.
//
// Wiring (one column of the shorting-bar switch):
//
//	Row 1 → GND
//	Row 2 → pinA (pull-up)
//	Row 3 → pinB (pull-up)
//	Row 4 → GND
//
// Position → GPIO state → Mode:
//
//	AM  (rows 1+2 bridged): A=LOW,  B=HIGH → ModePodcast
//	AFC (rows 2+3 bridged): A=HIGH, B=HIGH → ModeSpeaker
//	FM  (rows 3+4 bridged): A=HIGH, B=LOW  → ModeMusic
type Toggle struct {
	bus      *events.Bus
	pinNameA string
	pinNameB string

	mu      sync.Mutex
	stopCh  chan struct{}
	stopped bool
}

func NewToggle(bus *events.Bus, pinA, pinB string) *Toggle {
	return &Toggle{
		bus:      bus,
		pinNameA: pinA,
		pinNameB: pinB,
		stopCh:   make(chan struct{}),
	}
}

func (t *Toggle) Start() error {
	if _, err := host.Init(); err != nil {
		return fmt.Errorf("toggle: periph host init: %w", err)
	}

	pinA := gpioreg.ByName(t.pinNameA)
	if pinA == nil {
		return fmt.Errorf("toggle: GPIO pin %q not found", t.pinNameA)
	}
	pinB := gpioreg.ByName(t.pinNameB)
	if pinB == nil {
		return fmt.Errorf("toggle: GPIO pin %q not found", t.pinNameB)
	}

	for _, p := range []gpio.PinIn{pinA, pinB} {
		if err := p.In(gpio.PullUp, gpio.NoEdge); err != nil {
			return fmt.Errorf("toggle: configure pin: %w", err)
		}
	}

	go t.poll(pinA, pinB)
	return nil
}

func (t *Toggle) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	t.stopped = true
	close(t.stopCh)
}

func (t *Toggle) poll(pinA, pinB gpio.PinIn) {
	ticker := time.NewTicker(togglePollInterval)
	defer ticker.Stop()

	last := events.Mode("") // sentinel: emit on first read

	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			mode := readToggleMode(pinA, pinB)
			if mode == last {
				continue
			}
			last = mode
			t.bus.Publish(events.Event{Kind: events.KindToggleSwitched, Mode: mode})
			slog.Debug("toggle: switched", "mode", mode)
		}
	}
}

// readToggleMode determines the switch position from pins A and B.
//
//	A=LOW            → AM  → ModePodcast
//	A=HIGH, B=LOW    → FM  → ModeMusic
//	A=HIGH, B=HIGH   → AFC → ModeSpeaker
func readToggleMode(pinA, pinB gpio.PinIn) events.Mode {
	if pinA.Read() == gpio.Low {
		return events.ModePodcast
	}
	if pinB.Read() == gpio.Low {
		return events.ModeMusic
	}
	return events.ModeSpeaker
}
