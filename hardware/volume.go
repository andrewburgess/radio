//go:build pi

package hardware

import (
	"fmt"
	"log/slog"
	"math"
	"os/exec"
	"sync"
	"time"

	"andrewburgess.io/radio/events"
	"periph.io/x/conn/v3/physic"
	"periph.io/x/conn/v3/spi"
	"periph.io/x/conn/v3/spi/spireg"
	"periph.io/x/host/v3"
)

const (
	volumePollInterval = 100 * time.Millisecond
	volumeHysteresis   = 3    // minimum % change before calling amixer
	volumeWindowSize   = 8    // rolling average window to smooth ADC noise
	mcp3008MaxValue    = 1023 // 10-bit ADC
)

// Volume reads the volume potentiometer via the MCP3008 ADC over SPI,
// maps the value to 0–maxPct%, applies hysteresis to avoid thrashing amixer,
// and publishes KindVolumeChanged events.
//
// minRaw/maxRaw calibrate the physical pot range (the raw ADC values at the
// fully-down and fully-up positions). maxPct caps the output so full rotation
// doesn't blow out the speakers.
type Volume struct {
	bus         *events.Bus
	spiDev      string
	spiChannel  int
	alsaCard    string
	alsaControl string
	minRaw      int
	maxRaw      int
	maxPct      int
	curve       float64 // power curve exponent: <1 front-loads ramp, 1.0 = linear

	mu      sync.Mutex
	stopCh  chan struct{}
	stopped bool
}

func NewVolume(bus *events.Bus, spiDev string, spiChannel int, alsaCard, alsaControl string, minRaw, maxRaw, maxPct int, curve float64) *Volume {
	return &Volume{
		bus:         bus,
		spiDev:      spiDev,
		spiChannel:  spiChannel,
		alsaCard:    alsaCard,
		alsaControl: alsaControl,
		minRaw:      minRaw,
		maxRaw:      maxRaw,
		maxPct:      maxPct,
		curve:       curve,
		stopCh:      make(chan struct{}),
	}
}

func (v *Volume) Start() error {
	if _, err := host.Init(); err != nil {
		return fmt.Errorf("volume: periph host init: %w", err)
	}

	port, err := spireg.Open(v.spiDev)
	if err != nil {
		return fmt.Errorf("volume: open SPI device %q: %w", v.spiDev, err)
	}

	// MCP3008: 1 MHz, SPI Mode 0, 8 bits per word.
	conn, err := port.Connect(physic.MegaHertz, spi.Mode0, 8)
	if err != nil {
		port.Close()
		return fmt.Errorf("volume: connect SPI: %w", err)
	}

	go v.poll(conn, port)
	return nil
}

func (v *Volume) Stop() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.stopped {
		return
	}
	v.stopped = true
	close(v.stopCh)
}

func (v *Volume) poll(conn spi.Conn, port interface{ Close() error }) {
	defer port.Close()

	ticker := time.NewTicker(volumePollInterval)
	defer ticker.Stop()

	lastPct := -1 // sentinel: force amixer call on first read

	for {
		select {
		case <-v.stopCh:
			return
		case <-ticker.C:
			raw, err := v.readADC(conn)
			if err != nil {
				slog.Warn("volume: read ADC", "err", err)
				continue
			}

			pct := v.rawToPct(raw)
			if abs(pct-lastPct) < volumeHysteresis {
				continue
			}
			lastPct = pct

			if err := v.setAlsaVolume(pct); err != nil {
				slog.Warn("volume: set ALSA volume", "err", err)
			}
			v.bus.Publish(events.Event{Kind: events.KindVolumeChanged, Volume: pct})
			slog.Debug("volume: changed", "pct", pct)
		}
	}
}

// rawToPct maps a raw ADC value to a volume percentage using the calibrated
// range [minRaw, maxRaw], applies a power curve, and caps the result at maxPct.
// curve < 1.0 front-loads the ramp (e.g. 0.5 = square root); 1.0 = linear.
func (v *Volume) rawToPct(raw int) int {
	span := v.maxRaw - v.minRaw
	if span <= 0 {
		return 0
	}
	norm := float64(raw-v.minRaw) / float64(span)
	if norm < 0 {
		norm = 0
	}
	if norm > 1 {
		norm = 1
	}
	curved := math.Pow(norm, v.curve)
	return int(curved * float64(v.maxPct))
}

// readADC reads the configured MCP3008 channel using the standard 3-byte SPI
// transaction and returns the 10-bit result (0–1023).
func (v *Volume) readADC(conn spi.Conn) (int, error) {
	// MCP3008 single-ended read protocol:
	//   TX: [start bit=0x01] [SGL=1, D2..D0=channel, padding] [don't care]
	//   RX: [don't care]     [null bit + bits 9..8]            [bits 7..0]
	ch := byte(v.spiChannel & 0x07)
	tx := []byte{0x01, (0x80 | ch<<4), 0x00}
	rx := make([]byte, 3)
	if err := conn.Tx(tx, rx); err != nil {
		return 0, err
	}
	result := int(rx[1]&0x03)<<8 | int(rx[2])
	return result, nil
}

// setAlsaVolume calls amixer to set the hardware mixer volume.
func (v *Volume) setAlsaVolume(pct int) error {
	arg := fmt.Sprintf("%d%%", pct)
	cmd := exec.Command("amixer", "-c", v.alsaCard, "sset", v.alsaControl, arg)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("amixer: %w: %s", err, out)
	}
	return nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
