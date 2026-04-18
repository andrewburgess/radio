//go:build !pi

package audio

// AudioContext is a no-op on non-Pi builds where ALSA/oto is not available.
type AudioContext struct{}

func NewAudioContext(_ []string) (*AudioContext, error) { return &AudioContext{}, nil }
