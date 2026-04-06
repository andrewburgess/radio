package spotify

import "time"

// RadioTime calculates where in a playlist "the radio" currently is, as if
// the playlist has been playing on a loop continuously since the Unix epoch.
//
// It returns the zero-based index of the current track and the playback
// position within that track in milliseconds.
//
// If tracks is empty or all durations are zero, it returns (0, 0).
func RadioTime(tracks []Track, now time.Time) (trackIndex int, positionMs int) {
	if len(tracks) == 0 {
		return 0, 0
	}

	var totalMs int64
	for _, t := range tracks {
		totalMs += int64(t.DurationMs)
	}
	if totalMs == 0 {
		return 0, 0
	}

	offsetMs := now.UnixMilli() % totalMs

	var cumulative int64
	for i, t := range tracks {
		end := cumulative + int64(t.DurationMs)
		if offsetMs < end {
			return i, int(offsetMs - cumulative)
		}
		cumulative = end
	}

	// Unreachable: offsetMs < totalMs guarantees a match above.
	return len(tracks) - 1, 0
}
