//go:build pi

package hardware

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"andrewburgess.io/radio/events"
	"periph.io/x/periph/conn/i2c"
	"periph.io/x/periph/conn/i2c/i2creg"
	"periph.io/x/periph/host"
)

const (
	dialPollInterval = 100 * time.Millisecond
	dialDebounceN    = 3 // bucket must be stable for this many polls before emitting

	// TODO: verify these register addresses against the TMAG5273 datasheet.
	// The TMAG5273 must have angle calculation enabled in SENSOR_CONFIG_1
	// (ANGLE_EN bits set to 01 for X-Y) before these registers are valid.
	tmag5273RegSensorConfig1 = 0x02
	tmag5273RegAngleMSB      = 0x13 // ANGLE_RESULT[11:4]
	tmag5273RegAngleLSB      = 0x14 // ANGLE_RESULT[3:0] in upper nibble

	// SENSOR_CONFIG_1: ANGLE_EN = 01 (X-Y plane), rest default.
	// TODO: verify this value on hardware.
	tmag5273SensorConfig1Val = 0x08
)

// Dial reads the TMAG5273 Hall effect sensor over I2C, maps the angle to a
// bucket, debounces, and publishes KindDialMoved events.
type Dial struct {
	bus         *events.Bus
	i2cBus      string
	i2cAddr     uint16
	bucketCount int
	minAngle    float64
	maxAngle    float64

	mu      sync.Mutex
	stopCh  chan struct{}
	stopped bool
}

func NewDial(bus *events.Bus, i2cBus, i2cAddr string, bucketCount int, minAngle, maxAngle float64) *Dial {
	addr := parseHexAddr(i2cAddr, 0x22)
	return &Dial{
		bus:         bus,
		i2cBus:      i2cBus,
		i2cAddr:     addr,
		bucketCount: bucketCount,
		minAngle:    minAngle,
		maxAngle:    maxAngle,
		stopCh:      make(chan struct{}),
	}
}

func (d *Dial) Start() error {
	if _, err := host.Init(); err != nil {
		return fmt.Errorf("dial: periph host init: %w", err)
	}

	busRef, err := i2creg.Open(d.i2cBus)
	if err != nil {
		return fmt.Errorf("dial: open I2C bus %q: %w", d.i2cBus, err)
	}

	dev := &i2c.Dev{Bus: busRef, Addr: d.i2cAddr}

	// Enable angle calculation in SENSOR_CONFIG_1.
	// TODO: verify register address and value on hardware.
	if err := dev.Tx([]byte{tmag5273RegSensorConfig1, tmag5273SensorConfig1Val}, nil); err != nil {
		busRef.Close()
		return fmt.Errorf("dial: configure TMAG5273: %w", err)
	}

	go d.poll(dev, busRef)
	return nil
}

func (d *Dial) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	d.stopped = true
	close(d.stopCh)
}

func (d *Dial) poll(dev *i2c.Dev, busRef interface{ Close() error }) {
	defer busRef.Close()

	ticker := time.NewTicker(dialPollInterval)
	defer ticker.Stop()

	lastEmitted := -1
	candidate := -1
	stable := 0

	for {
		select {
		case <-d.stopCh:
			return
		case <-ticker.C:
			angle, err := d.readAngle(dev)
			if err != nil {
				slog.Warn("dial: read angle", "err", err)
				continue
			}

			bucket := d.angleToBucket(angle)

			if bucket == candidate {
				stable++
			} else {
				candidate = bucket
				stable = 1
			}

			if stable >= dialDebounceN && bucket != lastEmitted {
				lastEmitted = bucket
				d.bus.Publish(events.Event{Kind: events.KindDialMoved, Bucket: bucket})
				slog.Debug("dial: moved", "bucket", bucket, "angle", angle)
			}
		}
	}
}

func (d *Dial) readAngle(dev *i2c.Dev) (float64, error) {
	// Read two bytes starting at ANGLE_RESULT_MSB.
	// TODO: verify register layout on hardware.
	buf := make([]byte, 2)
	if err := dev.Tx([]byte{tmag5273RegAngleMSB}, buf); err != nil {
		return 0, err
	}

	// ANGLE_RESULT is a 12-bit value: MSB holds [11:4], LSB holds [3:0] in [7:4].
	raw := binary.BigEndian.Uint16(buf)
	raw12 := raw >> 4
	degrees := float64(raw12) * 360.0 / 4096.0
	return degrees, nil
}

func (d *Dial) angleToBucket(angle float64) int {
	span := d.maxAngle - d.minAngle
	if span <= 0 {
		return 0
	}
	norm := (angle - d.minAngle) / span
	norm = math.Max(0, math.Min(1, norm))
	bucket := int(norm * float64(d.bucketCount))
	if bucket >= d.bucketCount {
		bucket = d.bucketCount - 1
	}
	return bucket
}

// parseHexAddr parses a hex string like "0x22" into a uint16. Returns
// defaultVal on any parse error.
func parseHexAddr(s string, defaultVal uint16) uint16 {
	var v uint64
	if _, err := fmt.Sscanf(s, "0x%x", &v); err != nil {
		return defaultVal
	}
	return uint16(v)
}
