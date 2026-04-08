//go:build !pi

package hardware

import "andrewburgess.io/radio/events"

// Power is a no-op stub used in non-Pi builds. It emits a single
// PowerChanged event with PowerOn: true on Start.
type Power struct {
	bus *events.Bus
}

func NewPower(bus *events.Bus, gpioPin string) *Power {
	return &Power{bus: bus}
}

func (p *Power) Start() error {
	p.bus.Publish(events.Event{Kind: events.KindPowerChanged, PowerOn: true})
	return nil
}

func (p *Power) Stop() {}
