//go:build pi

package hardware

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"andrewburgess.io/radio/events"
	"periph.io/x/periph/conn/gpio"
	"periph.io/x/periph/conn/gpio/gpioreg"
	"periph.io/x/periph/host"
)

const togglePollInterval = 100 * time.Millisecond

// Toggle reads the AM/FM GPIO pin and publishes KindToggleSwitched events.
// High = music mode, Low = podcast mode. TODO: verify polarity on hardware.
type Toggle struct {
	bus     *events.Bus
	pinName string

	mu      sync.Mutex
	stopCh  chan struct{}
	stopped bool
}

func NewToggle(bus *events.Bus, gpioPin string) *Toggle {
	return &Toggle{
		bus:     bus,
		pinName: gpioPin,
		stopCh:  make(chan struct{}),
	}
}

func (t *Toggle) Start() error {
	if _, err := host.Init(); err != nil {
		return fmt.Errorf("toggle: periph host init: %w", err)
	}

	pin := gpioreg.ByName(t.pinName)
	if pin == nil {
		return fmt.Errorf("toggle: GPIO pin %q not found", t.pinName)
	}
	if err := pin.In(gpio.PullUp, gpio.NoEdge); err != nil {
		return fmt.Errorf("toggle: configure pin %q: %w", t.pinName, err)
	}

	go t.poll(pin)
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

func (t *Toggle) poll(pin gpio.PinIn) {
	ticker := time.NewTicker(togglePollInterval)
	defer ticker.Stop()

	last := gpio.High // start with an invalid sentinel so we emit on first read

	for {
		select {
		case <-t.stopCh:
			return
		case <-ticker.C:
			level := pin.Read()
			if level == last {
				continue
			}
			last = level

			mode := events.ModeMusic
			if level == gpio.Low {
				mode = events.ModePodcast
			}
			t.bus.Publish(events.Event{Kind: events.KindToggleSwitched, Mode: mode})
			slog.Debug("toggle: switched", "mode", mode)
		}
	}
}
