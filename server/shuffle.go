package server

import (
	"context"
	"log/slog"
	"time"

	"andrewburgess.io/radio/store"
)

// runShuffleScheduler sleeps until the next configured check hour, then calls
// runShuffleCheck. Repeats daily until the process exits.
func (s *Server) runShuffleScheduler() {
	for {
		d := durationUntilHour(s.cfg.ShuffleCheckHour)
		slog.Debug("shuffle scheduler: sleeping", "until", time.Now().Add(d).Format("15:04"))
		time.Sleep(d)
		s.runShuffleCheck()
	}
}

// runShuffleCheck iterates all music stations that have a shuffle interval set,
// skips any that are currently playing or not yet due, and shuffles the rest.
func (s *Server) runShuffleCheck() {
	stations, err := s.store.ListStations("music")
	if err != nil {
		slog.Error("shuffle scheduler: list stations", "err", err)
		return
	}

	activeURI := s.state.currentPlaylistURI()

	for _, st := range stations {
		if st.ShuffleIntervalDays == 0 || st.PlaylistURI == "" {
			continue
		}
		if !shuffleDue(st) {
			continue
		}
		if st.PlaylistURI == activeURI {
			slog.Info("shuffle scheduler: skipping active playlist", "bucket", st.Bucket, "uri", st.PlaylistURI)
			continue
		}

		slog.Info("shuffle scheduler: shuffling playlist", "bucket", st.Bucket, "uri", st.PlaylistURI)
		if err := s.spotify.ShufflePlaylist(context.Background(), st.PlaylistURI); err != nil {
			slog.Error("shuffle scheduler: shuffle failed", "bucket", st.Bucket, "uri", st.PlaylistURI, "err", err)
			continue
		}
		if err := s.store.RecordShuffle(st.Bucket, "music"); err != nil {
			slog.Error("shuffle scheduler: record shuffle", "bucket", st.Bucket, "err", err)
		}
		slog.Info("shuffle scheduler: done", "bucket", st.Bucket, "uri", st.PlaylistURI)
	}
}

// shuffleDue returns true if the station has never been shuffled or its
// shuffle interval has elapsed since the last shuffle.
func shuffleDue(st store.Station) bool {
	if st.LastShuffledAt == nil {
		return true
	}
	return time.Since(*st.LastShuffledAt) >= time.Duration(st.ShuffleIntervalDays)*24*time.Hour
}

// durationUntilHour returns the duration from now until the next occurrence of
// the given hour (0-23) in local time.
func durationUntilHour(hour int) time.Duration {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return time.Until(next)
}
