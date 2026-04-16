//go:build pi

// dial-calibrate sweeps the TMAG5273 Hall effect sensor and records raw
// X/Y/Z samples to a CSV file as you rotate the dial from stop to stop.
// Press Enter at each position you want to mark (e.g. bucket boundaries).
// Press Ctrl+C when the sweep is complete — the tool will print calibration
// constants (center offset, arc min/max, suggested bucket counts) derived
// from the locus of measured XY values.
//
// The tool configures 32× hardware oversampling on the chip to reduce
// per-sample noise before any data is recorded.
//
// Usage:
//
//	CGO_ENABLED=1 go run -tags pi ./cmd/dial-calibrate/ [outfile.csv]
package main

import (
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/host/v3"
)

// TMAG5273 register addresses.
const (
	regDeviceConfig1 = 0x00 // bits [6:4]: CONV_AVG (oversampling)
	regDeviceConfig2 = 0x01 // bits [1:0]: operating mode
	regSensorConfig1 = 0x02 // bits [7:4]: channel enable
	regSensorConfig2 = 0x03 // bits [3:2]: angle enable, [1]: XY range, [0]: Z range
	regXMSB          = 0x12 // X-axis result MSB (reads 2 bytes: MSB + LSB)
	regYMSB          = 0x14 // Y-axis result MSB
	regZMSB          = 0x16 // Z-axis result MSB
)

// Configuration values.
const (
	// DEVICE_CONFIG_1: CONV_AVG = 32× (bits [6:4] = 0x5 → 0x50).
	// Hardware-averages 32 conversions before reporting, dramatically
	// reducing per-sample noise with no software windowing required.
	cfgAvg32x = 0x50

	cfgContinuous    = 0x02 // DEVICE_CONFIG_2: continuous measurement mode
	cfgChannelsXYZ   = 0x70 // SENSOR_CONFIG_1: enable X+Y+Z (0x7 << 4)
	cfgAngleAndRange = 0x07 // SENSOR_CONFIG_2: XY angle | 80mT XY | 80mT Z
)

type sample struct {
	ts      int64
	x, y, z int16
	mark    int // 0 = normal; N = Nth mark recorded at this sample
}

func main() {
	outfile := fmt.Sprintf("dial-calibrate-%d.csv", time.Now().Unix())
	if len(os.Args) > 1 {
		outfile = os.Args[1]
	}

	if _, err := host.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "periph init: %v\n", err)
		os.Exit(1)
	}

	bus, err := i2creg.Open("I2C1")
	if err != nil {
		fmt.Fprintf(os.Stderr, "open I2C bus: %v\n", err)
		os.Exit(1)
	}
	defer bus.Close()

	dev := &i2c.Dev{Bus: bus, Addr: 0x22}

	if err := configureSensor(dev); err != nil {
		fmt.Fprintf(os.Stderr, "configure sensor: %v\n", err)
		os.Exit(1)
	}
	// Allow one full conversion cycle to complete before reading.
	time.Sleep(50 * time.Millisecond)

	f, err := os.Create(outfile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "create %s: %v\n", outfile, err)
		os.Exit(1)
	}
	defer f.Close()

	cw := csv.NewWriter(f)
	cw.Write([]string{"timestamp_ms", "x", "y", "z", "mark"}) //nolint

	fmt.Printf("Output:  %s\n\n", outfile)
	fmt.Println("Instructions:")
	fmt.Println("  1. Slowly rotate the dial from one physical stop to the other.")
	fmt.Println("  2. Press Enter to mark a position (e.g. each bucket boundary).")
	fmt.Println("  3. Press Ctrl+C when the sweep is complete.\n")
	fmt.Printf("%-10s  %-8s  %-8s  %-8s  %-12s\n", "elapsed", "X", "Y", "Z", "raw atan2°")
	fmt.Println("----------  --------  --------  --------  ------------")

	// Goroutine: block on stdin; send to markCh each time Enter is pressed.
	markCh := make(chan struct{}, 32)
	go func() {
		buf := make([]byte, 1)
		for {
			if _, err := os.Stdin.Read(buf); err != nil {
				return
			}
			if buf[0] == '\n' || buf[0] == '\r' {
				markCh <- struct{}{}
			}
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var (
		samples     []sample
		markSeq     int
		pendingMark int
		start       = time.Now()
	)

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-sigCh:
			break loop

		case <-markCh:
			markSeq++
			pendingMark = markSeq
			// Print on a new line so the mark notice isn't overwritten by \r.
			fmt.Printf("\n  [mark %d — move to next position, then press Enter again]\n",
				markSeq)

		case <-ticker.C:
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

			mark := pendingMark
			pendingMark = 0

			ts := time.Now().UnixMilli()
			elapsed := time.Since(start).Round(time.Millisecond)

			samples = append(samples, sample{ts: ts, x: x, y: y, z: z, mark: mark})

			atan2deg := math.Atan2(float64(x), float64(y)) * 180 / math.Pi
			if atan2deg < 0 {
				atan2deg += 360
			}

			fmt.Printf("\r%-10s  %-8d  %-8d  %-8d  %-12.1f",
				elapsed, x, y, z, atan2deg)

			cw.Write([]string{ //nolint
				strconv.FormatInt(ts, 10),
				strconv.Itoa(int(x)),
				strconv.Itoa(int(y)),
				strconv.Itoa(int(z)),
				strconv.Itoa(mark),
			})
			cw.Flush()
		}
	}

	fmt.Println("\n")
	analyze(samples)
}

// configureSensor writes the four setup registers in order.
func configureSensor(dev *i2c.Dev) error {
	steps := []struct {
		reg, val byte
		name     string
	}{
		{regDeviceConfig1, cfgAvg32x, "32× hardware averaging"},
		{regSensorConfig1, cfgChannelsXYZ, "XYZ channels"},
		{regSensorConfig2, cfgAngleAndRange, "XY angle + 80mT range"},
		{regDeviceConfig2, cfgContinuous, "continuous mode"},
	}
	for _, s := range steps {
		if err := dev.Tx([]byte{s.reg, s.val}, nil); err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return nil
}

// analyze computes calibration constants from the collected sweep samples.
//
// Strategy: use the bounding-box midpoint of the XY locus as the magnetic
// center estimate. This is more reliable than the sample mean for a partial
// arc because the mean is biased toward the dense middle of the sweep. Once
// centered, unwrap the angle sequence to handle the 0°/360° boundary, then
// report the arc extents and suggested bucket counts.
func analyze(samples []sample) {
	if len(samples) < 10 {
		fmt.Println("Too few samples to analyze (need at least 10).")
		return
	}

	// Bounding-box center as the magnetic-center estimate.
	minX, maxX := samples[0].x, samples[0].x
	minY, maxY := samples[0].y, samples[0].y
	for _, s := range samples[1:] {
		if s.x < minX {
			minX = s.x
		}
		if s.x > maxX {
			maxX = s.x
		}
		if s.y < minY {
			minY = s.y
		}
		if s.y > maxY {
			maxY = s.y
		}
	}
	cx := float64(minX+maxX) / 2.0
	cy := float64(minY+maxY) / 2.0

	fmt.Println("--- Locus analysis ---")
	fmt.Printf("X range:   [%d, %d]  (span %d)\n", minX, maxX, maxX-minX)
	fmt.Printf("Y range:   [%d, %d]  (span %d)\n", minY, maxY, maxY-minY)
	fmt.Printf("Estimated magnetic center:  cx=%.1f  cy=%.1f\n\n", cx, cy)

	// Compute raw centered angles (0–360°).
	raw := make([]float64, len(samples))
	for i, s := range samples {
		a := math.Atan2(float64(s.x)-cx, float64(s.y)-cy) * 180 / math.Pi
		if a < 0 {
			a += 360
		}
		raw[i] = a
	}

	// Unwrap: keep the sequence monotonic across the 0°/360° boundary.
	unwrapped := make([]float64, len(raw))
	unwrapped[0] = raw[0]
	for i := 1; i < len(raw); i++ {
		diff := raw[i] - unwrapped[i-1]
		if diff > 180 {
			diff -= 360
		} else if diff < -180 {
			diff += 360
		}
		unwrapped[i] = unwrapped[i-1] + diff
	}

	// Use the full min/max of the unwrapped sequence, not just the endpoints.
	// This is robust against the sweep ending slightly short of the physical
	// stop, or Ctrl+C being pressed before the final position is held steady.
	lo, hi := unwrapped[0], unwrapped[0]
	for _, a := range unwrapped[1:] {
		if a < lo {
			lo = a
		}
		if a > hi {
			hi = a
		}
	}
	span := hi - lo

	duration := time.Duration(samples[len(samples)-1].ts-samples[0].ts) * time.Millisecond
	fmt.Printf("Sweep arc:  %.1f° → %.1f°  (span %.1f°)\n", lo, hi, span)
	fmt.Printf("Samples:    %d over %s\n\n", len(samples), duration.Round(time.Second))

	fmt.Println("Bucket options:")
	fmt.Println("  count   deg/bucket")
	fmt.Println("  -----   ----------")
	for _, n := range []int{6, 8, 10, 12, 16, 20} {
		fmt.Printf("  %5d   %8.1f°\n", n, span/float64(n))
	}

	fmt.Println("\nCalibration constants — add these to your .env:")
	fmt.Printf("  DIAL_CENTER_X=%.0f\n", cx)
	fmt.Printf("  DIAL_CENTER_Y=%.0f\n", cy)
	fmt.Printf("  DIAL_MIN_ANGLE=%.1f\n", lo)
	fmt.Printf("  DIAL_MAX_ANGLE=%.1f\n", hi)

	// Report any marked positions.
	var markAngles []float64
	for i, s := range samples {
		if s.mark > 0 {
			markAngles = append(markAngles, unwrapped[i])
		}
	}
	if len(markAngles) > 0 {
		fmt.Printf("\nMarked positions (%d):\n", len(markAngles))
		for i, a := range markAngles {
			pct := (a - lo) / span * 100
			fmt.Printf("  Mark %d:  %.1f°  (%.1f%% of sweep)\n", i+1, a, pct)
		}
	}
}

// readInt16 reads two bytes starting at reg and returns them as a big-endian
// signed 16-bit integer.
func readInt16(dev *i2c.Dev, reg byte) (int16, error) {
	buf := make([]byte, 2)
	if err := dev.Tx([]byte{reg}, buf); err != nil {
		return 0, err
	}
	return int16(binary.BigEndian.Uint16(buf)), nil
}
