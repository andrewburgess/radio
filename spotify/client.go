package spotify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const apiBase = "https://api.spotify.com/v1"

// Track represents a Spotify track with the fields needed for radio time
// calculation and display.
type Track struct {
	URI        string
	Name       string
	DurationMs int
	Artists    []string
	Album      string
}

// Episode represents a Spotify podcast episode.
type Episode struct {
	URI         string
	Name        string
	DurationMs  int
	ShowName    string
	PublishedAt time.Time
}

// Device represents a Spotify Connect device (e.g. the librespot instance).
type Device struct {
	ID       string
	Name     string
	IsActive bool
}

// Client wraps the Spotify Web API with automatic token management.
type Client struct {
	auth *Auth
	http *http.Client
}

func NewClient(auth *Auth) *Client {
	return &Client{
		auth: auth,
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

// Auth returns the Auth instance used by this client. Use it to access the
// OAuth flow (AuthURL, Exchange) and token state (HasToken).
func (c *Client) Auth() *Auth {
	return c.auth
}

// GetDevices returns all Spotify Connect devices visible to the user.
func (c *Client) GetDevices(ctx context.Context) ([]Device, error) {
	var resp struct {
		Devices []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			IsActive bool   `json:"is_active"`
		} `json:"devices"`
	}
	if err := c.get(ctx, "/me/player/devices", &resp); err != nil {
		return nil, fmt.Errorf("spotify: get devices: %w", err)
	}
	devices := make([]Device, len(resp.Devices))
	for i, d := range resp.Devices {
		devices[i] = Device{ID: d.ID, Name: d.Name, IsActive: d.IsActive}
	}
	return devices, nil
}

// Play starts playback of contextURI (a playlist or show URI) on deviceID,
// beginning at track offsetIndex with an initial seek to positionMs.
// If deviceID is empty, Spotify targets the currently active device.
func (c *Client) Play(ctx context.Context, deviceID, contextURI string, offsetIndex, positionMs int) error {
	path := "/me/player/play"
	if deviceID != "" {
		path += "?device_id=" + deviceID
	}
	body := map[string]any{
		"context_uri": contextURI,
		"offset":      map[string]any{"position": offsetIndex},
		"position_ms": positionMs,
	}
	if err := c.put(ctx, path, body); err != nil {
		return fmt.Errorf("spotify: play: %w", err)
	}
	return nil
}

// Pause pauses playback on deviceID (or the active device if empty).
func (c *Client) Pause(ctx context.Context, deviceID string) error {
	path := "/me/player/pause"
	if deviceID != "" {
		path += "?device_id=" + deviceID
	}
	if err := c.put(ctx, path, nil); err != nil {
		return fmt.Errorf("spotify: pause: %w", err)
	}
	return nil
}

// Skip skips to the next track on deviceID (or the active device if empty).
func (c *Client) Skip(ctx context.Context, deviceID string) error {
	path := "/me/player/next"
	if deviceID != "" {
		path += "?device_id=" + deviceID
	}
	if err := c.post(ctx, path, nil); err != nil {
		return fmt.Errorf("spotify: skip: %w", err)
	}
	return nil
}

// GetCurrentTrack returns the currently playing track or episode, or nil if
// nothing is playing.
func (c *Client) GetCurrentTrack(ctx context.Context) (*Track, error) {
	var resp struct {
		Item struct {
			URI        string `json:"uri"`
			Name       string `json:"name"`
			DurationMs int    `json:"duration_ms"`
			Artists    []struct {
				Name string `json:"name"`
			} `json:"artists"`
			Album struct {
				Name string `json:"name"`
			} `json:"album"`
		} `json:"item"`
	}
	if err := c.get(ctx, "/me/player", &resp); err != nil {
		return nil, fmt.Errorf("spotify: get current track: %w", err)
	}
	if resp.Item.URI == "" {
		return nil, nil
	}
	artists := make([]string, len(resp.Item.Artists))
	for i, a := range resp.Item.Artists {
		artists[i] = a.Name
	}
	return &Track{
		URI:        resp.Item.URI,
		Name:       resp.Item.Name,
		DurationMs: resp.Item.DurationMs,
		Artists:    artists,
		Album:      resp.Item.Album.Name,
	}, nil
}

// GetPlaylistSnapshot returns the snapshot_id for a playlist — a cheap way to
// detect whether the playlist has changed without fetching the full track list.
// playlistURI may be a full Spotify URI, web URL, or bare ID.
func (c *Client) GetPlaylistSnapshot(ctx context.Context, playlistURI string) (string, error) {
	id := SpotifyID(playlistURI)
	if id == "" {
		return "", fmt.Errorf("spotify: invalid playlist URI %q", playlistURI)
	}
	var resp struct {
		SnapshotID string `json:"snapshot_id"`
	}
	if err := c.get(ctx, "/playlists/"+id+"?fields=snapshot_id", &resp); err != nil {
		return "", fmt.Errorf("spotify: get snapshot: %w", err)
	}
	return resp.SnapshotID, nil
}

// GetPlaylistTracks fetches all tracks in a playlist, following pagination.
// playlistURI may be a full Spotify URI (spotify:playlist:ID) or a bare ID.
func (c *Client) GetPlaylistTracks(ctx context.Context, playlistURI string) ([]Track, error) {
	id := SpotifyID(playlistURI)
	if id == "" {
		return nil, fmt.Errorf("spotify: invalid playlist URI %q", playlistURI)
	}

	var tracks []Track
	nextURL := fmt.Sprintf("%s/playlists/%s/tracks?limit=100", apiBase, id)

	for nextURL != "" {
		var page struct {
			Items []struct {
				Track *struct {
					URI        string `json:"uri"`
					Name       string `json:"name"`
					DurationMs int    `json:"duration_ms"`
					Artists    []struct {
						Name string `json:"name"`
					} `json:"artists"`
					Album struct {
						Name string `json:"name"`
					} `json:"album"`
				} `json:"track"`
			} `json:"items"`
			Next string `json:"next"`
		}
		if err := c.getURL(ctx, nextURL, &page); err != nil {
			return nil, fmt.Errorf("spotify: get playlist tracks: %w", err)
		}
		for _, item := range page.Items {
			if item.Track == nil || item.Track.URI == "" {
				continue // local tracks or null placeholders
			}
			artists := make([]string, len(item.Track.Artists))
			for i, a := range item.Track.Artists {
				artists[i] = a.Name
			}
			tracks = append(tracks, Track{
				URI:        item.Track.URI,
				Name:       item.Track.Name,
				DurationMs: item.Track.DurationMs,
				Artists:    artists,
				Album:      item.Track.Album.Name,
			})
		}
		nextURL = page.Next
	}
	return tracks, nil
}

// GetShowEpisodes fetches episodes from a show published on or after since,
// following pagination. Episodes are returned in reverse-chronological order
// (newest first) as Spotify provides them. showName is included in each
// returned Episode since the simplified episode object from the API does not
// carry the show name.
// showURI may be a full Spotify URI (spotify:show:ID) or a bare ID.
func (c *Client) GetShowEpisodes(ctx context.Context, showURI, showName string, since time.Time) ([]Episode, error) {
	id := SpotifyID(showURI)
	if id == "" {
		return nil, fmt.Errorf("spotify: invalid show URI %q", showURI)
	}

	var episodes []Episode
	nextURL := fmt.Sprintf("%s/shows/%s/episodes?limit=50", apiBase, id)

	for nextURL != "" {
		var page struct {
			Items []struct {
				URI         string `json:"uri"`
				Name        string `json:"name"`
				DurationMs  int    `json:"duration_ms"`
				ReleaseDate string `json:"release_date"`
			} `json:"items"`
			Next string `json:"next"`
		}
		if err := c.getURL(ctx, nextURL, &page); err != nil {
			return nil, fmt.Errorf("spotify: get show episodes: %w", err)
		}
		done := false
		for _, item := range page.Items {
			published, err := time.Parse("2006-01-02", item.ReleaseDate)
			if err != nil {
				continue
			}
			if published.Before(since) {
				done = true
				break
			}
			episodes = append(episodes, Episode{
				URI:         item.URI,
				Name:        item.Name,
				DurationMs:  item.DurationMs,
				ShowName:    showName,
				PublishedAt: published,
			})
		}
		if done {
			break
		}
		nextURL = page.Next
	}
	return episodes, nil
}

// GetTrackImage returns the URL of the album art for a track, preferring the
// largest available image. Returns an empty string if the track has no images.
// trackURI may be a full Spotify URI, web URL, or bare ID.
func (c *Client) GetTrackImage(ctx context.Context, trackURI string) (string, error) {
	id := SpotifyID(trackURI)
	if id == "" {
		return "", fmt.Errorf("spotify: invalid track URI %q", trackURI)
	}
	var resp struct {
		Album struct {
			Images []struct {
				URL string `json:"url"`
			} `json:"images"`
		} `json:"album"`
	}
	if err := c.get(ctx, "/tracks/"+id, &resp); err != nil {
		return "", fmt.Errorf("spotify: get track image: %w", err)
	}
	if len(resp.Album.Images) > 0 {
		return resp.Album.Images[0].URL, nil
	}
	return "", nil
}

// SetVolume sets the playback volume (0–100) on deviceID (or the active device
// if deviceID is empty).
func (c *Client) SetVolume(ctx context.Context, deviceID string, percent int) error {
	path := fmt.Sprintf("/me/player/volume?volume_percent=%d", percent)
	if deviceID != "" {
		path += "&device_id=" + deviceID
	}
	if err := c.put(ctx, path, nil); err != nil {
		return fmt.Errorf("spotify: set volume: %w", err)
	}
	return nil
}

// GetPlaylistInfo returns the name and cover image URL for a playlist in a
// single API call. imageURL is empty if the playlist has no images.
func (c *Client) GetPlaylistInfo(ctx context.Context, playlistURI string) (name, imageURL string, err error) {
	id := SpotifyID(playlistURI)
	if id == "" {
		return "", "", fmt.Errorf("spotify: invalid playlist URI %q", playlistURI)
	}
	var resp struct {
		Name   string `json:"name"`
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := c.get(ctx, "/playlists/"+id+"?fields=name,images", &resp); err != nil {
		return "", "", fmt.Errorf("spotify: get playlist info: %w", err)
	}
	if len(resp.Images) > 0 {
		imageURL = resp.Images[0].URL
	}
	return resp.Name, imageURL, nil
}

// GetPlaylistImage returns the URL of the cover image for a playlist,
// preferring the largest available image. Returns an empty string if the
// playlist has no images.
// playlistURI may be a full Spotify URI (spotify:playlist:ID) or a bare ID.
func (c *Client) GetPlaylistImage(ctx context.Context, playlistURI string) (string, error) {
	id := SpotifyID(playlistURI)
	if id == "" {
		return "", fmt.Errorf("spotify: invalid playlist URI %q", playlistURI)
	}
	var resp struct {
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := c.get(ctx, "/playlists/"+id+"?fields=images", &resp); err != nil {
		return "", fmt.Errorf("spotify: get playlist image: %w", err)
	}
	if len(resp.Images) > 0 {
		return resp.Images[0].URL, nil
	}
	return "", nil
}

// get makes a GET request to a path relative to apiBase and decodes the
// response into out.
func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.getURL(ctx, apiBase+path, out)
}

// getURL makes a GET request to a full URL and decodes the response into out.
func (c *Client) getURL(ctx context.Context, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	return c.do(req, out)
}

// put makes a PUT request with an optional JSON body.
func (c *Client) put(ctx context.Context, path string, body any) error {
	return c.sendJSON(ctx, http.MethodPut, apiBase+path, body)
}

// post makes a POST request with an optional JSON body.
func (c *Client) post(ctx context.Context, path string, body any) error {
	return c.sendJSON(ctx, http.MethodPost, apiBase+path, body)
}

func (c *Client) sendJSON(ctx context.Context, method, url string, body any) error {
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req, nil)
}

// do attaches the Authorization header and executes req. If out is non-nil,
// the response body is decoded as JSON into it.
func (c *Client) do(req *http.Request, out any) error {
	token, err := c.auth.AccessToken(req.Context())
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
		// success
	default:
		var apiErr struct {
			Error struct {
				Status  int    `json:"status"`
				Message string `json:"message"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&apiErr) //nolint:errcheck
		return fmt.Errorf("spotify API %d: %s", resp.StatusCode, apiErr.Error.Message)
	}

	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("spotify: decode response: %w", err)
		}
	}
	return nil
}

// SpotifyID extracts the resource ID from a Spotify URI, a Spotify web URL,
// or a bare ID. Returns an empty string if the input is unrecognisable.
//
//   - URI:     "spotify:playlist:4CcJtLqObbg4L5YEXaNrlY"                              → "4CcJtLqObbg4L5YEXaNrlY"
//   - URL:     "https://open.spotify.com/playlist/4CcJtLqObbg4L5YEXaNrlY?si=..."      → "4CcJtLqObbg4L5YEXaNrlY"
//   - Bare ID: "4CcJtLqObbg4L5YEXaNrlY"                                               → "4CcJtLqObbg4L5YEXaNrlY"
func SpotifyID(input string) string {
	// https://open.spotify.com/{type}/{id}?si=...
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		u, err := url.Parse(input)
		if err != nil {
			return ""
		}
		segments := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(segments) >= 2 {
			return segments[len(segments)-1]
		}
		return ""
	}

	// spotify:{type}:{id}
	if parts := strings.Split(input, ":"); len(parts) == 3 {
		return parts[2]
	}

	// Assume bare ID.
	return input
}
