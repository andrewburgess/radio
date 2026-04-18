//go:build !pi

package audio

import "context"

// InterstitialPlayer is a no-op on non-Pi builds where ALSA/oto is not available.
type InterstitialPlayer struct{}

func NewInterstitialPlayer(_ *AudioContext, _ string) *InterstitialPlayer {
	return &InterstitialPlayer{}
}

func (p *InterstitialPlayer) HasClips(_ string) bool                 { return false }
func (p *InterstitialPlayer) PickClip(_ string) (string, error)      { return "", nil }
func (p *InterstitialPlayer) Play(_ context.Context, _ string) error { return nil }
