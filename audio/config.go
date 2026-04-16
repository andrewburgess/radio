package audio

// Config holds the parameters for the static audio player.
type Config struct {
	// Files is the list of MP3 files to choose from. One is selected at random
	// each time Start is called and looped until Stop is called.
	Files []string
}
