// playlistinfo is a small diagnostic tool for inspecting a Spotify playlist.
// It prints the snapshot_id, total duration, and track list — useful for
// checking whether prompted playlists behave like regular playlists in the API.
//
// Usage:
//
//	go run ./cmd/playlistinfo <playlist-url-or-uri>
//
// Reads Spotify credentials from the same .env and radio.db as the main radio binary.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"andrewburgess.io/radio/config"
	"andrewburgess.io/radio/spotify"
	"andrewburgess.io/radio/store"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./cmd/playlistinfo <playlist-url-or-uri>")
		os.Exit(1)
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}

	db, err := store.New(cfg.DBPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "store: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	auth, err := spotify.NewAuth(
		cfg.SpotifyClientID,
		cfg.SpotifyClientSecret,
		cfg.SpotifyRedirectURI,
		db,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth: %v\n", err)
		os.Exit(1)
	}
	if !auth.HasToken() {
		fmt.Fprintln(os.Stderr, "not authenticated — run the radio server and visit /auth first")
		os.Exit(1)
	}

	client := spotify.NewClient(auth)
	ctx := context.Background()
	input := os.Args[1]

	// Fetch raw playlist metadata (includes snapshot_id which GetPlaylistTracks doesn't expose).
	meta, err := fetchPlaylistMeta(ctx, client, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch metadata: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Name:        %s\n", meta.Name)
	fmt.Printf("Owner:       %s\n", meta.Owner.DisplayName)
	fmt.Printf("Snapshot ID: %s\n", meta.SnapshotID)
	fmt.Printf("Tracks:      %d\n", meta.Tracks.Total)
	fmt.Printf("Description: %s\n", meta.Description)
	fmt.Printf("Public:      %v\n", meta.Public)
	fmt.Println()

	// Fetch full track list.
	fmt.Println("Fetching tracks...")
	tracks, err := client.GetPlaylistTracks(ctx, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fetch tracks: %v\n", err)
		os.Exit(1)
	}

	var totalMs int64
	for _, t := range tracks {
		totalMs += int64(t.DurationMs)
	}

	fmt.Printf("Total duration: %s\n\n", formatDuration(totalMs))

	for i, t := range tracks {
		fmt.Printf("[%3d] %s — %s (%s)\n",
			i,
			t.Name,
			strings.Join(t.Artists, ", "),
			formatDuration(int64(t.DurationMs)),
		)
	}

	// Print the raw metadata JSON so we can see all fields Spotify returns.
	fmt.Println("\n--- raw metadata JSON ---")
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(meta)
}

type playlistMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SnapshotID  string `json:"snapshot_id"`
	Public      bool   `json:"public"`
	Owner       struct {
		DisplayName string `json:"display_name"`
	} `json:"owner"`
	Tracks struct {
		Total int `json:"total"`
	} `json:"tracks"`
}

func fetchPlaylistMeta(ctx context.Context, client *spotify.Client, input string) (*playlistMeta, error) {
	token, err := client.Auth().AccessToken(ctx)
	if err != nil {
		return nil, err
	}

	id := spotify.SpotifyID(input)
	if id == "" {
		return nil, fmt.Errorf("could not parse playlist ID from %q", input)
	}

	url := "https://api.spotify.com/v1/playlists/" + id +
		"?fields=name,description,snapshot_id,public,owner,tracks.total"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Spotify API returned %d", resp.StatusCode)
	}

	var meta playlistMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func formatDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}
