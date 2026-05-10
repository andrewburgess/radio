package audio

// Config holds the parameters for the static audio player.
type Config struct {
	// Files is the list of MP3 files to choose from. One is selected at random
	// each time Start is called and looped until Stop is called.
	Files []string

	// GainMultiplier scales the logical gain (0–1) before writing PCM. Values
	// above 1.0 amplify the signal; PCM output is clamped to ±32767 so
	// clipping produces a crunchier, more prominent static sound.
	GainMultiplier float64
}
