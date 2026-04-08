package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// CachedPlaylist is one entry in the playlist cache.
type CachedPlaylist struct {
	SnapshotID      string    `json:"snapshot_id"`
	Tracks          []Track   `json:"tracks"`
	TotalDurationMs int64     `json:"total_duration_ms"`
	CachedAt        time.Time `json:"cached_at"`
}

// PlaylistCacheStore persists and retrieves cached playlist track lists.
// Implement this interface to swap in a different backend (e.g. SQLite in
// Phase 9).
type PlaylistCacheStore interface {
	Get(playlistURI string) (*CachedPlaylist, error)
	Set(playlistURI string, entry CachedPlaylist) error
	All() (map[string]CachedPlaylist, error)
}

// FilePlaylistCache implements PlaylistCacheStore using a single JSON file.
type FilePlaylistCache struct {
	path string
	mu   sync.Mutex
}

func NewFilePlaylistCache(path string) *FilePlaylistCache {
	return &FilePlaylistCache{path: path}
}

func (c *FilePlaylistCache) Get(playlistURI string) (*CachedPlaylist, error) {
	all, err := c.load()
	if err != nil {
		return nil, err
	}
	entry, ok := all[playlistURI]
	if !ok {
		return nil, nil
	}
	return &entry, nil
}

func (c *FilePlaylistCache) Set(playlistURI string, entry CachedPlaylist) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	all, err := c.load()
	if err != nil {
		return err
	}
	all[playlistURI] = entry
	return c.save(all)
}

func (c *FilePlaylistCache) All() (map[string]CachedPlaylist, error) {
	return c.load()
}

func (c *FilePlaylistCache) load() (map[string]CachedPlaylist, error) {
	data, err := os.ReadFile(c.path)
	if os.IsNotExist(err) {
		return make(map[string]CachedPlaylist), nil
	}
	if err != nil {
		return nil, fmt.Errorf("spotify: read playlist cache: %w", err)
	}
	var m map[string]CachedPlaylist
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("spotify: parse playlist cache: %w", err)
	}
	return m, nil
}

func (c *FilePlaylistCache) save(m map[string]CachedPlaylist) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("spotify: marshal playlist cache: %w", err)
	}
	if err := os.WriteFile(c.path, data, 0644); err != nil {
		return fmt.Errorf("spotify: write playlist cache: %w", err)
	}
	return nil
}

// GetTracksWithCache returns the track list for playlistURI, using the cache
// when the playlist's snapshot_id is unchanged. If the snapshot has changed
// (or there is no cached entry), it fetches the full track list, updates the
// cache, and returns the fresh data.
func (c *Client) GetTracksWithCache(ctx context.Context, playlistURI string, cache PlaylistCacheStore) ([]Track, error) {
	snapshotID, err := c.GetPlaylistSnapshot(ctx, playlistURI)
	if err != nil {
		return nil, fmt.Errorf("spotify: get snapshot: %w", err)
	}

	cached, err := cache.Get(playlistURI)
	if err != nil {
		return nil, fmt.Errorf("spotify: read cache: %w", err)
	}

	if cached != nil && cached.SnapshotID == snapshotID {
		slog.Debug("spotify: playlist cache hit", "uri", playlistURI)
		return cached.Tracks, nil
	}

	slog.Info("spotify: playlist cache miss, fetching tracks",
		"uri", playlistURI,
		"reason", cacheReason(cached, snapshotID),
	)

	tracks, err := c.GetPlaylistTracks(ctx, playlistURI)
	if err != nil {
		return nil, err
	}

	var totalMs int64
	for _, t := range tracks {
		totalMs += int64(t.DurationMs)
	}

	if err := cache.Set(playlistURI, CachedPlaylist{
		SnapshotID:      snapshotID,
		Tracks:          tracks,
		TotalDurationMs: totalMs,
		CachedAt:        time.Now(),
	}); err != nil {
		// Non-fatal: log and continue with the freshly fetched tracks.
		slog.Warn("spotify: failed to update playlist cache", "err", err)
	}

	return tracks, nil
}

func cacheReason(cached *CachedPlaylist, newSnapshotID string) string {
	if cached == nil {
		return "no cache entry"
	}
	if cached.SnapshotID != newSnapshotID {
		return "snapshot_id changed"
	}
	return "unknown"
}
