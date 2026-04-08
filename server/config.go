package server

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
)

// MusicConfig holds the mapping of dial buckets to Spotify playlist URIs for music mode.
type MusicConfig struct {
	mu   sync.RWMutex
	Path string            // Path to the JSON file (e.g., "music-config.json")
	URIs map[int]string    // Bucket index → Spotify playlist URI
}

// PodcastConfig holds the mapping of dial buckets to Spotify playlist URIs for podcast mode.
type PodcastConfig struct {
	mu   sync.RWMutex
	Path string            // Path to the JSON file (e.g., "podcast-config.json")
	URIs map[int]string    // Bucket index → Spotify playlist URI
}

// NewMusicConfig creates a MusicConfig and loads from disk if the file exists.
func NewMusicConfig(path string, bucketCount int) *MusicConfig {
	mc := &MusicConfig{
		Path: path,
		URIs: make(map[int]string, bucketCount),
	}
	if err := mc.load(); err != nil {
		slog.Warn("failed to load music config", "path", path, "err", err)
	}
	return mc
}

// NewPodcastConfig creates a PodcastConfig and loads from disk if the file exists.
func NewPodcastConfig(path string, bucketCount int) *PodcastConfig {
	pc := &PodcastConfig{
		Path: path,
		URIs: make(map[int]string, bucketCount),
	}
	if err := pc.load(); err != nil {
		slog.Warn("failed to load podcast config", "path", path, "err", err)
	}
	return pc
}

// Get returns the playlist URI for a bucket, or "" if unassigned.
func (mc *MusicConfig) Get(bucket int) string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return mc.URIs[bucket]
}

// Get returns the playlist URI for a bucket, or "" if unassigned.
func (pc *PodcastConfig) Get(bucket int) string {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.URIs[bucket]
}

// Set updates a bucket's playlist URI and persists to disk.
func (mc *MusicConfig) Set(bucket int, uri string) error {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if uri == "" {
		delete(mc.URIs, bucket)
	} else {
		mc.URIs[bucket] = uri
	}
	return mc.save()
}

// Set updates a bucket's playlist URI and persists to disk.
func (pc *PodcastConfig) Set(bucket int, uri string) error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if uri == "" {
		delete(pc.URIs, bucket)
	} else {
		pc.URIs[bucket] = uri
	}
	return pc.save()
}

// All returns a copy of all bucket assignments.
func (mc *MusicConfig) All() map[int]string {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	result := make(map[int]string, len(mc.URIs))
	for k, v := range mc.URIs {
		result[k] = v
	}
	return result
}

// All returns a copy of all bucket assignments.
func (pc *PodcastConfig) All() map[int]string {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	result := make(map[int]string, len(pc.URIs))
	for k, v := range pc.URIs {
		result[k] = v
	}
	return result
}

func (mc *MusicConfig) load() error {
	data, err := os.ReadFile(mc.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // File doesn't exist yet; that's fine
		}
		return err
	}
	return json.Unmarshal(data, &mc.URIs)
}

func (pc *PodcastConfig) load() error {
	data, err := os.ReadFile(pc.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // File doesn't exist yet; that's fine
		}
		return err
	}
	return json.Unmarshal(data, &pc.URIs)
}

func (mc *MusicConfig) save() error {
	data, err := json.MarshalIndent(mc.URIs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(mc.Path, data, 0644)
}

func (pc *PodcastConfig) save() error {
	data, err := json.MarshalIndent(pc.URIs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(pc.Path, data, 0644)
}
