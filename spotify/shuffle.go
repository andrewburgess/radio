package spotify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
)

// ShufflePlaylist fetches the current track list for playlistURI, computes a
// random permutation, and applies it via a sequence of reorder API calls.
// Each call passes the snapshot_id returned by the previous call so Spotify
// treats them as a consistent series. Returns after all moves are applied.
func (c *Client) ShufflePlaylist(ctx context.Context, playlistURI string) error {
	id := SpotifyID(playlistURI)
	if id == "" {
		return fmt.Errorf("spotify: invalid playlist URI %q", playlistURI)
	}

	snapshotID, err := c.GetPlaylistSnapshot(ctx, playlistURI)
	if err != nil {
		return err
	}

	tracks, err := c.GetPlaylistTracks(ctx, playlistURI)
	if err != nil {
		return err
	}

	if len(tracks) < 2 {
		return nil
	}

	current := make([]string, len(tracks))
	for i, t := range tracks {
		current[i] = t.URI
	}

	target := make([]string, len(current))
	copy(target, current)
	rand.Shuffle(len(target), func(i, j int) { target[i], target[j] = target[j], target[i] })

	moves := computeReorderMoves(current, target)
	slog.Info("spotify: shuffling playlist", "id", id, "tracks", len(tracks), "moves", len(moves))

	for _, move := range moves {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		snapshotID, err = c.reorderPlaylistTracks(ctx, id, move[0], move[1], snapshotID)
		if err != nil {
			return err
		}
	}

	return nil
}

// reorderPlaylistTracks moves the single item at rangeStart to position
// insertBefore in the playlist, using snapshotID for optimistic concurrency.
// Returns the new snapshot_id on success.
func (c *Client) reorderPlaylistTracks(ctx context.Context, playlistID string, rangeStart, insertBefore int, snapshotID string) (string, error) {
	body := map[string]any{
		"range_start":   rangeStart,
		"insert_before": insertBefore,
		"range_length":  1,
		"snapshot_id":   snapshotID,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		apiBase+"/playlists/"+playlistID+"/tracks", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	var resp struct {
		SnapshotID string `json:"snapshot_id"`
	}
	if err := c.do(req, &resp); err != nil {
		return "", fmt.Errorf("spotify: reorder playlist tracks: %w", err)
	}
	return resp.SnapshotID, nil
}

// computeReorderMoves returns the sequence of (rangeStart, insertBefore) pairs
// that transforms current into target using a selection-sort approach: for each
// position i, find where target[i] sits in the live order and move it there.
// Each pair maps directly to the Spotify reorder API's range_start and
// insert_before parameters (insert_before = i in all cases).
func computeReorderMoves(current, target []string) [][2]int {
	n := len(current)
	if n != len(target) {
		return nil
	}

	// origIdx[uri] = index in the initial current slice
	origIdx := make(map[string]int, n)
	for i, uri := range current {
		origIdx[uri] = i
	}

	// desired[i] = original index of the track that target wants at position i
	desired := make([]int, n)
	for i, uri := range target {
		desired[i] = origIdx[uri]
	}

	// pos[origIdx] = current live position of the track with that original index
	pos := make([]int, n)
	for i := range pos {
		pos[i] = i
	}

	var moves [][2]int
	for i := 0; i < n-1; i++ {
		p := pos[desired[i]]
		if p == i {
			continue
		}
		moves = append(moves, [2]int{p, i})

		// Update live positions after the move.
		// The Spotify reorder API removes the item at p then inserts it before i,
		// so insert_before = i regardless of whether p > i or p < i.
		if p > i {
			// Items at live positions [i, p-1] shift right by 1.
			for j := 0; j < n; j++ {
				if pos[j] >= i && pos[j] < p {
					pos[j]++
				}
			}
		} else {
			// Items at live positions [p+1, i] shift left by 1.
			for j := 0; j < n; j++ {
				if pos[j] > p && pos[j] <= i {
					pos[j]--
				}
			}
		}
		pos[desired[i]] = i
	}

	return moves
}
