//go:build pi

// dial-test continuously reads the TMAG5273 Hall effect sensor and prints raw
// X/Y/Z field values alongside the onboard angle calculation. Use it to verify
// wiring, determine which axis pair to use for atan2, and confirm register
// addresses before trusting the main dial code.
//
// Usage:
//
//	CGO_ENABLED=1 go run -tags pi ./cmd/dial-test/
package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"time"

	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/host/v3"
)

// TMAG5273 register addresses (from SparkFun library / datasheet).
const (
	regDeviceConfig2 = 0x01 // bits [1:0]: operating mode
	regSensorConfig1 = 0x02 // bits [7:4]: channel enable, bits [3:0]: sleep time
	regSensorConfig2 = 0x03 // bits [3:2]: angle enable, bit [1]: XY range, bit [0]: Z range
	regManufacturerL = 0x0E // should read 0x49
	regManufacturerM = 0x0F // should read 0x54 → together "TI"
	regDeviceID      = 0x0D
	regXMSB          = 0x12
	regYMSB          = 0x14
	regZMSB          = 0x16
	regConvStatus    = 0x18
	regAngleMSB      = 0x19 // onboard angle result [11:4]
	regAngleLSB      = 0x1A // onboard angle result [3:0] in upper nibble
)

// Configuration values.
const (
	// DEVICE_CONFIG_2: continuous measurement mode
	cfgContinuous = 0x02

	// SENSOR_CONFIG_1: enable X+Y+Z channels (0x7 << 4)
	cfgChannelsXYZ = 0x70

	// SENSOR_CONFIG_2: XY angle calculation (0x1<<2) | 80mT XY range (0x1<<1) | 80mT Z range (0x1<<0)
	cfgAngleAndRange = 0x07
)

func main() {
	if _, err := host.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "periph init: %v\n", err)
		os.Exit(1)
	}

	busName := "I2C1"
	if len(os.Args) > 1 {
		busName = os.Args[1]
	}

	bus, err := i2creg.Open(busName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open I2C bus %q: %v\n", busName, err)
		os.Exit(1)
	}
	defer bus.Close()

	dev := &i2c.Dev{Bus: bus, Addr: 0x22}

	// Confirm we're talking to a TMAG5273.
	mfgL := make([]byte, 1)
	mfgM := make([]byte, 1)
	devID := make([]byte, 1)
	dev.Tx([]byte{regManufacturerL}, mfgL)
	dev.Tx([]byte{regManufacturerM}, mfgM)
	dev.Tx([]byte{regDeviceID}, devID)
	fmt.Printf("Manufacturer ID: 0x%02X 0x%02X (expect 0x49 0x54 = \"TI\")\n", mfgL[0], mfgM[0])
	fmt.Printf("Device ID:       0x%02X     (expect 0x01 or 0x02)\n", devID[0])
	if mfgL[0] != 0x49 || mfgM[0] != 0x54 {
		fmt.Fprintln(os.Stderr, "WARNING: unexpected manufacturer ID — wrong address or wiring issue?")
	}

	// Enable X+Y+Z channels (bits [7:4] of SENSOR_CONFIG_1).
	if err := dev.Tx([]byte{regSensorConfig1, cfgChannelsXYZ}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "enable channels: %v\n", err)
		os.Exit(1)
	}

	// Enable XY angle calculation + 80mT range (SENSOR_CONFIG_2).
	if err := dev.Tx([]byte{regSensorConfig2, cfgAngleAndRange}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "set angle+range: %v\n", err)
		os.Exit(1)
	}

	// Set continuous measurement mode (DEVICE_CONFIG_2).
	if err := dev.Tx([]byte{regDeviceConfig2, cfgContinuous}, nil); err != nil {
		fmt.Fprintf(os.Stderr, "set continuous mode: %v\n", err)
		os.Exit(1)
	}

	// Give the sensor a moment to complete its first conversion.
	time.Sleep(50 * time.Millisecond)

	// Quick sanity check on DEVICE_STATUS.
	all := make([]byte, 0x1D)
	if err := dev.Tx([]byte{0x00}, all); err == nil {
		ds := all[0x1C]
		if ds&0x10 != 0 {
			fmt.Fprintln(os.Stderr, "WARNING: OTP CRC error (DEVICE_STATUS bit 4)")
		}
	}
	fmt.Println()
	fmt.Printf("%-8s  %-8s  %-8s  %-12s  %-10s\n", "X", "Y", "Z", "atan2(X,Y)°", "onboard°")
	fmt.Println("--------  --------  --------  ------------  ----------")

	fmt.Println("Reading TMAG5273 — spin the dial and watch the values.")

	const windowSize = 5
	type sample struct {
		x, y, z int16
		onboard  float64
	}
	window := make([]sample, 0, windowSize)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		x, err := readInt16(dev, regXMSB)
		if err != nil {
			continue
		}
		y, err := readInt16(dev, regYMSB)
		if err != nil {
			continue
		}
		z, err := readInt16(dev, regZMSB)
		if err != nil {
			continue
		}
		onboard, err := readOnboardAngle(dev)
		if err != nil {
			continue
		}

		if len(window) >= windowSize {
			window = window[1:]
		}
		window = append(window, sample{x, y, z, onboard})

		var sumX, sumY, sumZ int64
		var sumOnboard float64
		for _, s := range window {
			sumX += int64(s.x)
			sumY += int64(s.y)
			sumZ += int64(s.z)
			sumOnboard += s.onboard
		}
		n := float64(len(window))
		avgX := float64(sumX) / n
		avgY := float64(sumY) / n
		avgZ := float64(sumZ) / n
		avgOnboard := sumOnboard / n

		atan2deg := math.Atan2(avgX, avgY) * 180 / math.Pi
		if atan2deg < 0 {
			atan2deg += 360
		}

		fmt.Printf("\r%-8.0f  %-8.0f  %-8.0f  %-12.1f  %-10.1f", avgX, avgY, avgZ, atan2deg, avgOnboard)
	}
}

// readInt16 reads two bytes from addr and returns them as a signed 16-bit int.
func readInt16(dev *i2c.Dev, reg byte) (int16, error) {
	buf := make([]byte, 2)
	if err := dev.Tx([]byte{reg}, buf); err != nil {
		return 0, err
	}
	return int16(binary.BigEndian.Uint16(buf)), nil
}

// readOnboardAngle reads the TMAG5273's hardware-computed angle (0–360°).
// The result is a 12-bit value: MSB holds bits [11:4], LSB holds bits [3:0]
// in the upper nibble.
func readOnboardAngle(dev *i2c.Dev) (float64, error) {
	buf := make([]byte, 2)
	if err := dev.Tx([]byte{regAngleMSB}, buf); err != nil {
		return 0, err
	}
	raw := uint16(buf[0])<<4 | uint16(buf[1])>>4
	return float64(raw) * 360.0 / 4096.0, nil
}
