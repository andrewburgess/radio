package server

import (
	"testing"
)

func TestStationLabel(t *testing.T) {
	tests := []struct {
		name   string
		bucket int
		total  int
		mode   string
		want   string
	}{
		// FM endpoints with 12 buckets (default)
		// Channels snap to nearest North American FM frequency (odd tenths, 200 kHz spacing)
		{"fm first", 0, 12, "music", "87.9 FM"},
		{"fm last", 11, 12, "music", "107.9 FM"},
		{"fm middle", 5, 12, "music", "96.9 FM"},
		// AM endpoints with 12 buckets
		{"am first", 0, 12, "podcast", "550 AM"},
		{"am last", 11, 12, "podcast", "1600 AM"},
		// Edge: single bucket
		{"fm single", 0, 1, "music", "87.5 FM"},
		{"am single", 0, 1, "podcast", "550 AM"},
		// Speaker/AFC mode
		{"speaker", 5, 12, "speaker", "AFC"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stationLabel(tt.bucket, tt.total, tt.mode)
			if got != tt.want {
				t.Errorf("stationLabel(%d, %d, %q) = %q, want %q", tt.bucket, tt.total, tt.mode, got, tt.want)
			}
		})
	}
}
