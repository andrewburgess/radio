//go:build pi

package hardware

import (
	"fmt"

	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/host/v3"
)

// Amp controls the amplifier's SD (shutdown) pin.
// Drive HIGH to enable the amp; LOW to mute/shutdown and eliminate idle hiss.
// TODO: verify polarity — some amps are active-low shutdown (HIGH=on, LOW=off),
// others are active-high shutdown (HIGH=off, LOW=on).
type Amp struct {
	pinName string
	pin     gpio.PinIO
}

func NewAmp(gpioPin string) *Amp {
	return &Amp{pinName: gpioPin}
}

func (a *Amp) Start() error {
	if _, err := host.Init(); err != nil {
		return fmt.Errorf("amp: periph host init: %w", err)
	}

	pin := gpioreg.ByName(a.pinName)
	if pin == nil {
		return fmt.Errorf("amp: GPIO pin %q not found", a.pinName)
	}

	// Start muted — unmute when audio begins.
	if err := pin.Out(gpio.Low); err != nil {
		return fmt.Errorf("amp: set pin output: %w", err)
	}
	a.pin = pin
	return nil
}

// Unmute enables the amplifier (SD pin HIGH).
func (a *Amp) Unmute() {
	if a.pin != nil {
		a.pin.Out(gpio.High)
	}
}

// Mute disables the amplifier (SD pin LOW), eliminating idle hiss.
func (a *Amp) Mute() {
	if a.pin != nil {
		a.pin.Out(gpio.Low)
	}
}
