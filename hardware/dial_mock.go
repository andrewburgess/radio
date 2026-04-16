//go:build !pi

package hardware

import "andrewburgess.io/radio/events"

// Dial is a no-op stub used in non-Pi builds. It emits a single DialMoved
// event at bucket 0 on Start so downstream code has a known initial state.
type Dial struct {
	bus *events.Bus
}

func NewDial(bus *events.Bus, i2cBus, i2cAddr string, bucketCount int, centerX, centerY, minAngle, maxAngle float64) *Dial {
	return &Dial{bus: bus}
}

func (d *Dial) Start() error {
	d.bus.Publish(events.Event{Kind: events.KindDialMoved, Bucket: 0})
	return nil
}

func (d *Dial) Stop() {}
