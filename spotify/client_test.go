package spotify

import "testing"

func TestSpotifyID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Spotify URIs
		{
			name:  "playlist URI",
			input: "spotify:playlist:4CcJtLqObbg4L5YEXaNrlY",
			want:  "4CcJtLqObbg4L5YEXaNrlY",
		},
		{
			name:  "show URI",
			input: "spotify:show:5CnDmMUG0S5JtAjTM8YCNJ",
			want:  "5CnDmMUG0S5JtAjTM8YCNJ",
		},
		{
			name:  "track URI",
			input: "spotify:track:6rqhFgbbKwnb9MLmUQDhG6",
			want:  "6rqhFgbbKwnb9MLmUQDhG6",
		},

		// Spotify web URLs
		{
			name:  "playlist URL with si param",
			input: "https://open.spotify.com/playlist/4CcJtLqObbg4L5YEXaNrlY?si=bf54db635cda48c5",
			want:  "4CcJtLqObbg4L5YEXaNrlY",
		},
		{
			name:  "playlist URL without si param",
			input: "https://open.spotify.com/playlist/4CcJtLqObbg4L5YEXaNrlY",
			want:  "4CcJtLqObbg4L5YEXaNrlY",
		},
		{
			name:  "show URL",
			input: "https://open.spotify.com/show/5CnDmMUG0S5JtAjTM8YCNJ",
			want:  "5CnDmMUG0S5JtAjTM8YCNJ",
		},
		{
			name:  "episode URL with si param",
			input: "https://open.spotify.com/episode/0zLhl3WsOCQHbe1BPTiHgr?si=abc123",
			want:  "0zLhl3WsOCQHbe1BPTiHgr",
		},

		// Bare IDs - passed through unchanged
		{
			name:  "bare ID",
			input: "4CcJtLqObbg4L5YEXaNrlY",
			want:  "4CcJtLqObbg4L5YEXaNrlY",
		},

		// Edge cases
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "malformed URL",
			input: "https://",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SpotifyID(tt.input)
			if got != tt.want {
				t.Errorf("SpotifyID(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
