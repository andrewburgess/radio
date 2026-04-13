//go:build !pi

package hardware

import "andrewburgess.io/radio/events"

// Volume is a no-op stub used in non-Pi builds. It emits a single
// VolumeChanged event at 80% on Start and does not call amixer.
type Volume struct {
	bus *events.Bus
}

func NewVolume(bus *events.Bus, spiDev string, spiChannel int, alsaControl string, minRaw, maxRaw, maxPct int) *Volume {
	return &Volume{bus: bus}
}

func (v *Volume) Start() error {
	v.bus.Publish(events.Event{Kind: events.KindVolumeChanged, Volume: 80})
	return nil
}

func (v *Volume) Stop() {}
