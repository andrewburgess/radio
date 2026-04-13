//go:build !pi

package hardware

import "log/slog"

// Amp is a no-op stub for non-Pi builds.
type Amp struct {
	pinName string
}

func NewAmp(gpioPin string) *Amp {
	return &Amp{pinName: gpioPin}
}

func (a *Amp) Start() error { return nil }

func (a *Amp) Unmute() {
	slog.Debug("amp: unmute")
}

func (a *Amp) Mute() {
	slog.Debug("amp: mute")
}
