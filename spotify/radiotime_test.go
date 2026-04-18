package spotify

import (
	"testing"
	"time"
)

// msTime returns a time.Time whose UnixMilli equals ms, for test readability.
func msTime(ms int64) time.Time {
	return time.UnixMilli(ms)
}

func tracks(durations ...int) []Track {
	ts := make([]Track, len(durations))
	for i, d := range durations {
		ts[i] = Track{DurationMs: d}
	}
	return ts
}

func TestRadioTime_Empty(t *testing.T) {
	idx, pos := RadioTime(nil, msTime(12345))
	if idx != 0 || pos != 0 {
		t.Errorf("got (%d, %d), want (0, 0)", idx, pos)
	}
}

func TestRadioTime_SingleTrack(t *testing.T) {
	ts := tracks(60_000) // one 60-second track

	// Offset 0 -> start of track 0.
	idx, pos := RadioTime(ts, msTime(0))
	assertPlay(t, idx, pos, 0, 0)

	// Offset 30s into the track.
	idx, pos = RadioTime(ts, msTime(30_000))
	assertPlay(t, idx, pos, 0, 30_000)

	// Offset = duration wraps to start.
	idx, pos = RadioTime(ts, msTime(60_000))
	assertPlay(t, idx, pos, 0, 0)

	// Offset = 1.5x duration -> middle of second loop.
	idx, pos = RadioTime(ts, msTime(90_000))
	assertPlay(t, idx, pos, 0, 30_000)
}

func TestRadioTime_MultiTrack_Beginning(t *testing.T) {
	ts := tracks(10_000, 20_000, 30_000) // total 60s
	idx, pos := RadioTime(ts, msTime(0))
	assertPlay(t, idx, pos, 0, 0)
}

func TestRadioTime_MultiTrack_MidFirstTrack(t *testing.T) {
	ts := tracks(10_000, 20_000, 30_000)
	idx, pos := RadioTime(ts, msTime(5_000))
	assertPlay(t, idx, pos, 0, 5_000)
}

func TestRadioTime_MultiTrack_ExactTrackBoundary(t *testing.T) {
	ts := tracks(10_000, 20_000, 30_000)
	// Offset = 10s -> start of track 1 (track 0 ends at exactly 10s).
	idx, pos := RadioTime(ts, msTime(10_000))
	assertPlay(t, idx, pos, 1, 0)
}

func TestRadioTime_MultiTrack_MidSecondTrack(t *testing.T) {
	ts := tracks(10_000, 20_000, 30_000)
	// Offset = 15s -> 5s into track 1.
	idx, pos := RadioTime(ts, msTime(15_000))
	assertPlay(t, idx, pos, 1, 5_000)
}

func TestRadioTime_MultiTrack_LastTrack(t *testing.T) {
	ts := tracks(10_000, 20_000, 30_000)
	// Offset = 45s -> 15s into track 2.
	idx, pos := RadioTime(ts, msTime(45_000))
	assertPlay(t, idx, pos, 2, 15_000)
}

func TestRadioTime_MultiTrack_WrapAround(t *testing.T) {
	ts := tracks(10_000, 20_000, 30_000) // total 60s
	// Offset = 70s -> wraps to 10s -> start of track 1.
	idx, pos := RadioTime(ts, msTime(70_000))
	assertPlay(t, idx, pos, 1, 0)
}

func TestRadioTime_AllZeroDurations(t *testing.T) {
	ts := tracks(0, 0, 0)
	idx, pos := RadioTime(ts, msTime(99_999))
	if idx != 0 || pos != 0 {
		t.Errorf("got (%d, %d), want (0, 0)", idx, pos)
	}
}

func assertPlay(t *testing.T, gotIdx, gotPos, wantIdx, wantPos int) {
	t.Helper()
	if gotIdx != wantIdx || gotPos != wantPos {
		t.Errorf("got (track=%d, pos=%d), want (track=%d, pos=%d)",
			gotIdx, gotPos, wantIdx, wantPos)
	}
}
