//go:build pi

package audio

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	mp3 "github.com/hajimehoshi/go-mp3"
)

// InterstitialPlayer picks and plays one-shot MP3 clips through the shared oto
// context. Clips are organized on disk as:
//
//	<dir>/<playlist-uri-slug>/*.mp3
//
// where the slug is the last colon-separated segment of the playlist URI
// (e.g. "spotify:playlist:ABC123" -> "ABC123").
type InterstitialPlayer struct {
	audioCtx *AudioContext
	dir      string
}

func NewInterstitialPlayer(ctx *AudioContext, dir string) *InterstitialPlayer {
	return &InterstitialPlayer{audioCtx: ctx, dir: dir}
}

// HasClips reports whether any MP3 clips exist for the given playlist URI.
func (p *InterstitialPlayer) HasClips(playlistURI string) bool {
	clips, err := p.listClips(playlistURI)
	return err == nil && len(clips) > 0
}

// PickClip returns the path to a randomly selected clip for playlistURI.
func (p *InterstitialPlayer) PickClip(playlistURI string) (string, error) {
	clips, err := p.listClips(playlistURI)
	if err != nil {
		return "", err
	}
	if len(clips) == 0 {
		return "", fmt.Errorf("no clips for %s", playlistURI)
	}
	return clips[rand.Intn(len(clips))], nil
}

// Play opens clipPath, decodes it as MP3, and plays it through the shared oto
// context. Blocks until the clip finishes or ctx is cancelled. Returns nil on
// clean completion; ctx.Err() if cancelled.
func (p *InterstitialPlayer) Play(ctx context.Context, clipPath string) error {
	otoCtx := p.audioCtx.oto

	f, err := os.Open(clipPath)
	if err != nil {
		return err
	}
	defer f.Close()

	dec, err := mp3.NewDecoder(f)
	if err != nil {
		return err
	}

	player := otoCtx.NewPlayer(dec)
	defer player.Close()
	player.Play()
	slog.Debug("interstitial: playing", "clip", filepath.Base(clipPath))

	const checkInterval = 50 * time.Millisecond
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !player.IsPlaying() {
				return nil
			}
		}
	}
}

func (p *InterstitialPlayer) listClips(playlistURI string) ([]string, error) {
	slug := SlugForURI(playlistURI)
	if slug == "" {
		return nil, fmt.Errorf("invalid playlist URI: %q", playlistURI)
	}
	dir := filepath.Join(p.dir, slug)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var clips []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp3") {
			clips = append(clips, filepath.Join(dir, e.Name()))
		}
	}
	return clips, nil
}
