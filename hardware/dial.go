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
	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/host/v3"
)

const (
	dialPollInterval = 100 * time.Millisecond
	dialDebounceN    = 3 // bucket must be stable for this many polls before emitting

	// TMAG5273 register addresses (verified against SparkFun library).
	tmag5273RegDeviceConfig1 = 0x00 // bits [6:4]: CONV_AVG (oversampling count)
	tmag5273RegDeviceConfig2 = 0x01 // bits [1:0]: operating mode
	tmag5273RegSensorConfig1 = 0x02 // bits [7:4]: channel enable, bits [3:0]: sleep time
	tmag5273RegSensorConfig2 = 0x03 // bits [3:2]: angle enable, bit [1]: XY range, bit [0]: Z range
	tmag5273RegXMSB          = 0x12 // X-axis result MSB (reads 2 bytes: MSB + LSB)
	tmag5273RegYMSB          = 0x14 // Y-axis result MSB

	// Configuration values.
	tmag5273Avg32x        = 0x50 // DEVICE_CONFIG_1: CONV_AVG = 32× (bits [6:4] = 0x5)
	tmag5273Continuous    = 0x02 // DEVICE_CONFIG_2: continuous measurement mode
	tmag5273ChannelsXYZ   = 0x70 // SENSOR_CONFIG_1: enable X+Y+Z (0x7 << 4)
	tmag5273AngleAndRange = 0x07 // SENSOR_CONFIG_2: XY angle | 80mT XY range | 80mT Z range
)

// Dial reads the TMAG5273 Hall effect sensor over I2C, maps the angle to a
// bucket, debounces, and publishes KindDialMoved events.
//
// Angle is computed as atan2(x-centerX, y-centerY), correcting for off-center
// magnet mounting. centerX and centerY are the bounding-box midpoint of the
// XY locus measured during calibration (output by cmd/dial-calibrate).
type Dial struct {
	bus         *events.Bus
	i2cBus      string
	i2cAddr     uint16
	bucketCount int
	centerX     float64
	centerY     float64
	minAngle    float64
	maxAngle    float64

	mu      sync.Mutex
	stopCh  chan struct{}
	stopped bool
}

func NewDial(bus *events.Bus, i2cBus, i2cAddr string, bucketCount int, centerX, centerY, minAngle, maxAngle float64) *Dial {
	addr := parseHexAddr(i2cAddr, 0x22)
	return &Dial{
		bus:         bus,
		i2cBus:      i2cBus,
		i2cAddr:     addr,
		bucketCount: bucketCount,
		centerX:     centerX,
		centerY:     centerY,
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

	// Configure sensor registers in order.
	setup := []struct {
		reg, val byte
		name     string
	}{
		{tmag5273RegDeviceConfig1, tmag5273Avg32x, "32× averaging"},
		{tmag5273RegSensorConfig1, tmag5273ChannelsXYZ, "XYZ channels"},
		{tmag5273RegSensorConfig2, tmag5273AngleAndRange, "XY angle + 80mT range"},
		{tmag5273RegDeviceConfig2, tmag5273Continuous, "continuous mode"},
	}
	for _, s := range setup {
		if err := dev.Tx([]byte{s.reg, s.val}, nil); err != nil {
			busRef.Close()
			return fmt.Errorf("dial: %s: %w", s.name, err)
		}
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
			slog.Debug("dial: poll", "angle", angle, "bucket", bucket, "candidate", candidate, "stable", stable)

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
	x, err := readSensorInt16(dev, tmag5273RegXMSB)
	if err != nil {
		return 0, err
	}
	y, err := readSensorInt16(dev, tmag5273RegYMSB)
	if err != nil {
		return 0, err
	}

	deg := math.Atan2(float64(x)-d.centerX, float64(y)-d.centerY) * 180 / math.Pi
	if deg < 0 {
		deg += 360
	}

	// If the calibrated arc straddles the 0°/360° boundary, snap the reading
	// to the same side of the circle as the arc midpoint.
	mid := (d.minAngle + d.maxAngle) / 2.0
	for deg-mid > 180 {
		deg -= 360
	}
	for mid-deg > 180 {
		deg += 360
	}

	return deg, nil
}

// readSensorInt16 reads two bytes starting at reg and returns them as a
// big-endian signed 16-bit integer.
func readSensorInt16(dev *i2c.Dev, reg byte) (int16, error) {
	buf := make([]byte, 2)
	if err := dev.Tx([]byte{reg}, buf); err != nil {
		return 0, err
	}
	return int16(binary.BigEndian.Uint16(buf)), nil
}

func (d *Dial) angleToBucket(angle float64) int {
	span := d.maxAngle - d.minAngle
	if span == 0 {
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
