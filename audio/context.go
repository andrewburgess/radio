//go:build pi

package audio

import (
	"fmt"
	"os"

	"github.com/ebitengine/oto/v3"
	mp3 "github.com/hajimehoshi/go-mp3"
)

// AudioContext wraps the shared oto audio context. Create exactly one per
// process and pass it to all audio players — oto only permits one context
// per audio device.
type AudioContext struct {
	oto *oto.Context
}

// NewAudioContext reads the sample rate from the first file in files, then
// creates and waits for the oto audio context. All audio files played through
// this context must be encoded at that same sample rate.
func NewAudioContext(files []string) (*AudioContext, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("audio: at least one file required to determine sample rate")
	}
	sampleRate, err := readSampleRate(files[0])
	if err != nil {
		return nil, fmt.Errorf("audio: read sample rate from %s: %w", files[0], err)
	}
	ctx, ready, err := oto.NewContext(&oto.NewContextOptions{
		SampleRate:   sampleRate,
		ChannelCount: 2,
		Format:       oto.FormatSignedInt16LE,
	})
	if err != nil {
		return nil, fmt.Errorf("audio: create oto context: %w", err)
	}
	<-ready
	return &AudioContext{oto: ctx}, nil
}

func readSampleRate(file string) (int, error) {
	f, err := os.Open(file)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	dec, err := mp3.NewDecoder(f)
	if err != nil {
		return 0, err
	}
	return dec.SampleRate(), nil
}
