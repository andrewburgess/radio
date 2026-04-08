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

const powerPollInterval = 100 * time.Millisecond

// Power reads the volume knob's integrated power switch GPIO pin and publishes
// KindPowerChanged events.
// High = power on, Low = power off. TODO: verify polarity on hardware.
type Power struct {
	bus     *events.Bus
	pinName string

	mu      sync.Mutex
	stopCh  chan struct{}
	stopped bool
}

func NewPower(bus *events.Bus, gpioPin string) *Power {
	return &Power{
		bus:     bus,
		pinName: gpioPin,
		stopCh:  make(chan struct{}),
	}
}

func (p *Power) Start() error {
	if _, err := host.Init(); err != nil {
		return fmt.Errorf("power: periph host init: %w", err)
	}

	pin := gpioreg.ByName(p.pinName)
	if pin == nil {
		return fmt.Errorf("power: GPIO pin %q not found", p.pinName)
	}
	if err := pin.In(gpio.PullUp, gpio.NoEdge); err != nil {
		return fmt.Errorf("power: configure pin %q: %w", p.pinName, err)
	}

	// Emit initial state immediately.
	initial := pin.Read() == gpio.High
	p.bus.Publish(events.Event{Kind: events.KindPowerChanged, PowerOn: initial})

	go p.poll(pin)
	return nil
}

func (p *Power) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.stopped = true
	close(p.stopCh)
}

func (p *Power) poll(pin gpio.PinIn) {
	ticker := time.NewTicker(powerPollInterval)
	defer ticker.Stop()

	last := pin.Read()

	for {
		select {
		case <-p.stopCh:
			return
		case <-ticker.C:
			level := pin.Read()
			if level == last {
				continue
			}
			last = level

			on := level == gpio.High
			p.bus.Publish(events.Event{Kind: events.KindPowerChanged, PowerOn: on})
			slog.Info("power: state changed", "on", on)
		}
	}
}
