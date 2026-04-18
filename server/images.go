package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"andrewburgess.io/radio/spotify"
)

var imageHTTPClient = &http.Client{Timeout: 30 * 1e9} // 30 seconds for CDN downloads

// localImageURL returns the server-local URL for playlistURI if an image is
// already cached on disk, otherwise "". Makes no network calls - safe to call
// on the HTTP response render path.
func (s *Server) localImageURL(playlistURI string) string {
	_, fileName, ok, err := s.store.GetImageCache(playlistURI)
	if err != nil {
		slog.Warn("images: read image cache", "uri", playlistURI, "err", err)
		return ""
	}
	if !ok {
		return ""
	}
	if _, statErr := os.Stat(filepath.Join(s.imageDir, fileName)); statErr != nil {
		return ""
	}
	return "/images/" + fileName
}

// cachedPlaylistImage returns the server-local URL for a playlist's cover image
// (e.g. "/images/4CcJtLqObbg4L5YEXaNrlY.jpg"). It uses snapshot_id to detect
// stale entries and only re-downloads when the snapshot has changed or the file
// is missing from disk.
//
// All network calls use context.Background() - never call this on the HTTP
// response render path; use localImageURL there and call this from a goroutine.
func (s *Server) cachedPlaylistImage(playlistURI string) string {
	ctx := context.Background()

	imageURL, snapshotID, err := s.spotify.GetPlaylistImageAndSnapshot(ctx, playlistURI)
	if err != nil {
		slog.Warn("images: fetch playlist cover info", "uri", playlistURI, "err", err)
		return ""
	}
	if imageURL == "" {
		return ""
	}

	cachedSnapshot, fileName, ok, err := s.store.GetImageCache(playlistURI)
	if err != nil {
		slog.Warn("images: read image cache", "uri", playlistURI, "err", err)
	}

	if ok && cachedSnapshot == snapshotID {
		if _, statErr := os.Stat(filepath.Join(s.imageDir, fileName)); statErr == nil {
			return "/images/" + fileName
		}
		slog.Debug("images: cached file missing from disk, re-downloading", "file", fileName)
	}

	id := spotify.SpotifyID(playlistURI)
	fileName, err = s.downloadImage(ctx, imageURL, id)
	if err != nil {
		slog.Warn("images: download failed", "uri", playlistURI, "err", err)
		return ""
	}

	if err := s.store.SetImageCache(playlistURI, snapshotID, fileName); err != nil {
		slog.Warn("images: update image cache", "uri", playlistURI, "err", err)
	}

	return "/images/" + fileName
}

// downloadImage fetches imageURL from the CDN, writes it to imageDir as
// {id}.jpg (or .png based on Content-Type), and returns the file name.
func (s *Server) downloadImage(ctx context.Context, imageURL, id string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}

	resp, err := imageHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	ext := ".jpg"
	if ct := resp.Header.Get("Content-Type"); ct == "image/png" {
		ext = ".png"
	}

	if err := os.MkdirAll(s.imageDir, 0755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}

	fileName := id + ext
	path := filepath.Join(s.imageDir, fileName)

	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("write: %w", err)
	}

	slog.Info("images: cached playlist cover", "id", id, "file", fileName)
	return fileName, nil
}

// handleImages serves cached playlist cover images from the image cache directory.
func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	fileName := r.PathValue("filename")
	// Reject empty names and any path traversal attempts.
	if fileName == "" || filepath.Base(fileName) != fileName {
		http.NotFound(w, r)
		return
	}
	ext := filepath.Ext(fileName)
	if ext != ".jpg" && ext != ".png" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.imageDir, fileName))
}
