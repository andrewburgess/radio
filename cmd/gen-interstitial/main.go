// gen-interstitial is an interactive CLI for generating DJ interstitial clips
// via the ElevenLabs TTS API and managing them per Spotify station.
//
// Usage: run from the repo/install root (same directory as .env and radio.db):
//
//	./gen-interstitial
//
// Required env var: ELEVENLABS_API_KEY
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"andrewburgess.io/radio/audio"
	"andrewburgess.io/radio/config"
	"andrewburgess.io/radio/spotify"
	"andrewburgess.io/radio/store"
)

const elevenLabsBase = "https://api.elevenlabs.io/v1"

func main() {
	cfg, err := config.Load()
	if err != nil {
		fatalf("config: %v", err)
	}

	db, err := store.New(cfg.DBPath)
	if err != nil {
		fatalf("db: %v", err)
	}
	defer db.Close()

	auth, err := spotify.NewAuth(cfg.SpotifyClientID, cfg.SpotifyClientSecret, cfg.SpotifyRedirectURI, db)
	if err != nil {
		fatalf("spotify auth: %v", err)
	}
	if !auth.HasToken() {
		fatalf("Spotify not authorized - start the radio server and visit /auth first")
	}
	elevenKey := os.Getenv("ELEVENLABS_API_KEY")
	if elevenKey == "" {
		fatalf("ELEVENLABS_API_KEY not set")
	}

	el := &elevenLabs{apiKey: elevenKey}
	runStationMenu(cfg, db, el)
}

// --- Station menu ---

type stationEntry struct {
	Bucket          int
	PlaylistURI     string
	PlaylistName    string
	ClipCount       int
	TrackCount      int
	TotalDurationMs int64
}

func runStationMenu(cfg *config.Config, db *store.Store, el *elevenLabs) {
	for {
		stations := loadStations(db, cfg.InterstitialDir)

		fmt.Println()
		fmt.Println("=== Zenith Radio - Interstitial Generator ===")
		fmt.Println()

		if len(stations) == 0 {
			fmt.Println("  No music stations assigned. Configure stations in the radio web UI first.")
			return
		}

		fmt.Printf("  %-3s  %-32s  %-6s  %-7s  %s\n", "#", "Station", "Bucket", "Tracks", "Clips")
		fmt.Println("  " + strings.Repeat("─", 62))
		for i, s := range stations {
			name := s.PlaylistName
			if name == "" {
				name = s.PlaylistURI
			}
			if len(name) > 32 {
				name = name[:29] + "..."
			}
			tracks := "-"
			if s.TrackCount > 0 {
				tracks = fmt.Sprintf("%d", s.TrackCount)
			}
			fmt.Printf("  %-3d  %-32s  %-6d  %-7s  %d\n", i+1, name, s.Bucket, tracks, s.ClipCount)
		}
		fmt.Println()

		choice := prompt("Select station [1-%d, q=quit]: ", len(stations))
		if choice == "q" || choice == "" {
			fmt.Println()
			return
		}
		idx := parseIdx(choice, len(stations))
		if idx < 0 {
			fmt.Println("  Invalid selection.")
			continue
		}
		runClipMenu(stations[idx], cfg.InterstitialDir, el)
	}
}

// --- Clip menu ---

func runClipMenu(station stationEntry, interstitialDir string, el *elevenLabs) {
	clipDir := filepath.Join(interstitialDir, audio.SlugForURI(station.PlaylistURI))

	for {
		clips := listClips(clipDir)
		name := station.PlaylistName
		if name == "" {
			name = station.PlaylistURI
		}

		fmt.Println()
		if station.TrackCount > 0 {
			dur := time.Duration(station.TotalDurationMs) * time.Millisecond
			fmt.Printf("=== %s [bucket %d] - %d tracks (%s) - %d clip(s) ===\n",
				name, station.Bucket, station.TrackCount, formatDuration(dur), len(clips))
		} else {
			fmt.Printf("=== %s [bucket %d] - %d clip(s) ===\n", name, station.Bucket, len(clips))
		}
		fmt.Println()

		for i, c := range clips {
			fmt.Printf("  %d  %s\n", i+1, filepath.Base(c))
		}
		if len(clips) > 0 {
			fmt.Println()
		}

		fmt.Println("  n  Generate new clip")
		fmt.Println("  b  Back")
		fmt.Println()

		choice := prompt("Choice: ")
		switch {
		case choice == "b" || choice == "":
			return
		case choice == "n":
			runGenerate(station, clipDir, el)
		default:
			idx := parseIdx(choice, len(clips))
			if idx < 0 {
				fmt.Println("  Invalid choice.")
				continue
			}
			runClipActions(clips[idx], station, clipDir, el)
		}
	}
}

// --- Generate ---

func runGenerate(station stationEntry, clipDir string, el *elevenLabs) {
	fmt.Println()

	voiceID, ok := pickVoice(el)
	if !ok {
		return
	}

	fmt.Println()
	fmt.Println("Enter DJ copy (blank line to finish):")
	text := readMultiLine()
	if strings.TrimSpace(text) == "" {
		fmt.Println("  No text entered.")
		return
	}

	outPath, err := generate(el, voiceID, text, clipDir)
	if err != nil {
		fmt.Printf("  Error: %v\n", err)
		return
	}
	runPostGenerate(outPath, voiceID, station, clipDir, el)
}

func generate(el *elevenLabs, voiceID, text, clipDir string) (string, error) {
	fmt.Print("  Generating... ")
	data, err := el.textToSpeech(voiceID, text)
	if err != nil {
		fmt.Println("failed.")
		return "", err
	}
	fmt.Println("done!")

	if err := os.MkdirAll(clipDir, 0755); err != nil {
		return "", fmt.Errorf("create directory: %w", err)
	}
	filename := fmt.Sprintf("interstitial-%d.mp3", time.Now().Unix())
	outPath := filepath.Join(clipDir, filename)
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return "", fmt.Errorf("save file: %w", err)
	}
	fmt.Printf("  Saved: %s\n", outPath)
	return outPath, nil
}

func runPostGenerate(path, voiceID string, station stationEntry, clipDir string, el *elevenLabs) {
	for {
		fmt.Println()
		fmt.Println("  p  Play")
		fmt.Println("  k  Keep and go back")
		fmt.Println("  r  Regenerate (new text, same voice)")
		fmt.Println("  v  Regenerate (new text, different voice)")
		fmt.Println("  d  Discard")
		fmt.Println()

		switch prompt("Choice: ") {
		case "p":
			playClip(path)
		case "k", "":
			return
		case "r":
			os.Remove(path) //nolint:errcheck
			fmt.Println()
			fmt.Println("Enter DJ copy (blank line to finish):")
			text := readMultiLine()
			if strings.TrimSpace(text) == "" {
				fmt.Println("  No text entered.")
				return
			}
			newPath, err := generate(el, voiceID, text, clipDir)
			if err != nil {
				fmt.Printf("  Error: %v\n", err)
				return
			}
			path = newPath
		case "v":
			os.Remove(path) //nolint:errcheck
			runGenerate(station, clipDir, el)
			return
		case "d":
			os.Remove(path) //nolint:errcheck
			fmt.Println("  Discarded.")
			return
		}
	}
}

// --- Existing clip actions ---

func runClipActions(path string, station stationEntry, clipDir string, el *elevenLabs) {
	for {
		fmt.Println()
		fmt.Printf("=== %s ===\n", filepath.Base(path))
		fmt.Println()
		fmt.Println("  p  Play")
		fmt.Println("  r  Regenerate (replace with new clip)")
		fmt.Println("  d  Delete")
		fmt.Println("  b  Back")
		fmt.Println()

		switch prompt("Choice: ") {
		case "p":
			playClip(path)
		case "r":
			os.Remove(path) //nolint:errcheck
			runGenerate(station, clipDir, el)
			return
		case "d":
			if err := os.Remove(path); err != nil {
				fmt.Printf("  error: %v\n", err)
			} else {
				fmt.Println("  Deleted.")
			}
			return
		case "b", "":
			return
		}
	}
}

// --- Voice picker ---

func pickVoice(el *elevenLabs) (string, bool) {
	for {
		search := prompt("  Search voices (Enter to show all): ")
		fmt.Print("  Loading voices... ")
		voices, err := el.searchVoices(search)
		if err != nil {
			fmt.Printf("failed: %v\n", err)
			return "", false
		}
		fmt.Printf("found %d\n", len(voices))

		if len(voices) == 0 {
			fmt.Println("  No voices match - try a different term.")
			continue
		}

		fmt.Println()
		fmt.Printf("  %-3s  %-28s  %s\n", "#", "Name", "Category")
		fmt.Println("  " + strings.Repeat("─", 46))
		for i, v := range voices {
			fmt.Printf("  %-3d  %-28s  %s\n", i+1, v.Name, v.Category)
		}
		fmt.Println()

		choice := prompt("Select voice [1-%d, s=search again]: ", len(voices))
		if choice == "s" {
			continue
		}
		idx := parseIdx(choice, len(voices))
		if idx < 0 {
			fmt.Println("  Invalid selection.")
			continue
		}
		fmt.Printf("  Using: %s\n", voices[idx].Name)
		return voices[idx].VoiceID, true
	}
}

// --- ElevenLabs client ---

type elevenLabs struct {
	apiKey string
}

type elVoice struct {
	VoiceID           string   `json:"voice_id"`
	Name              string   `json:"name"`
	Category          string   `json:"category"`
	AvailableForTiers []string `json:"available_for_tiers"`
}

// searchVoices fetches all matching voices from the v2 endpoint, paginating
// until has_more is false. Cloned voices sort first, then premade, both alpha.
func (el *elevenLabs) searchVoices(search string) ([]elVoice, error) {
	const pageSize = 100
	var all []elVoice
	nextToken := ""

	for {
		url := fmt.Sprintf("https://api.elevenlabs.io/v2/voices?page_size=%d&include_total_count=false", pageSize)
		if search != "" {
			url += "&search=" + search
		}
		if nextToken != "" {
			url += "&next_page_token=" + nextToken
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("xi-api-key", el.apiKey)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var page struct {
			Voices        []elVoice `json:"voices"`
			HasMore       bool      `json:"has_more"`
			NextPageToken string    `json:"next_page_token"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, err
		}
		for _, v := range page.Voices {
			if availableOnFree(v) {
				all = append(all, v)
			}
		}
		if !page.HasMore {
			break
		}
		nextToken = page.NextPageToken
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].Category != all[j].Category {
			return all[i].Category == "cloned"
		}
		return all[i].Name < all[j].Name
	})
	return all, nil
}

func (el *elevenLabs) textToSpeech(voiceID, text string) ([]byte, error) {
	body, err := json.Marshal(map[string]any{
		"text":     text,
		"model_id": "eleven_multilingual_v2",
	})
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/text-to-speech/%s?output_format=mp3_44100_128", elevenLabsBase, voiceID)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", el.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "audio/mpeg")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	return io.ReadAll(resp.Body)
}

// --- Helpers ---

func loadStations(db *store.Store, interstitialDir string) []stationEntry {
	stations, err := db.ListStations("music")
	if err != nil {
		fatalf("list stations: %v", err)
	}

	var entries []stationEntry
	for _, s := range stations {
		if s.PlaylistURI == "" {
			continue
		}
		name := s.Label
		if name == "" {
			name = audio.SlugForURI(s.PlaylistURI)
		}
		clipDir := filepath.Join(interstitialDir, audio.SlugForURI(s.PlaylistURI))
		entry := stationEntry{
			Bucket:       s.Bucket,
			PlaylistURI:  s.PlaylistURI,
			PlaylistName: name,
			ClipCount:    len(listClips(clipDir)),
		}
		if cached, err := db.Get(s.PlaylistURI); err == nil && cached != nil {
			entry.TrackCount = len(cached.Tracks)
			entry.TotalDurationMs = cached.TotalDurationMs
		}
		entries = append(entries, entry)
	}
	return entries
}

func availableOnFree(v elVoice) bool {
	for _, t := range v.AvailableForTiers {
		if t == "free" {
			return true
		}
	}
	return false
}

func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func listClips(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var clips []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mp3") {
			clips = append(clips, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(clips)
	return clips
}

func playClip(path string) {
	fmt.Printf("  Playing %s...\n", filepath.Base(path))
	cmd := exec.Command("ffplay", "-nodisp", "-autoexit", "-loglevel", "quiet", path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("  ffplay error: %v (is ffmpeg installed?)\n", err)
	}
}

var stdin = bufio.NewScanner(os.Stdin)

func prompt(format string, args ...any) string {
	fmt.Printf(format, args...)
	if stdin.Scan() {
		return strings.TrimSpace(stdin.Text())
	}
	return ""
}

func readMultiLine() string {
	var lines []string
	for stdin.Scan() {
		line := stdin.Text()
		if line == "" {
			break
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, " ")
}

// parseIdx converts a 1-based user choice string to a 0-based index,
// returning -1 if out of range or not a number.
func parseIdx(s string, max int) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	if n < 1 || n > max {
		return -1
	}
	return n - 1
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
