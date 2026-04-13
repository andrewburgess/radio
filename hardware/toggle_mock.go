//go:build !pi

package hardware

import "andrewburgess.io/radio/events"

// Toggle is a no-op stub used in non-Pi builds. It emits a single
// ToggleSwitched event in music mode on Start.
type Toggle struct {
	bus *events.Bus
}

func NewToggle(bus *events.Bus, pinA, pinB string) *Toggle {
	return &Toggle{bus: bus}
}

func (t *Toggle) Start() error {
	t.bus.Publish(events.Event{Kind: events.KindToggleSwitched, Mode: events.ModeMusic})
	return nil
}

func (t *Toggle) Stop() {}
