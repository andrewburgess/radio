package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"andrewburgess.io/radio/events"
)

// SSE event names. HTMX elements use these as sse-swap targets in Phase 8.
const (
	sseEventTrack    = "track"    // track or episode changed
	sseEventPlayback = "playback" // playing/paused state
	sseEventStatic   = "static"   // static audio on/off
	sseEventDial     = "dial"     // dial settled on a new bucket
	sseEventMode     = "mode"     // AM/FM toggle changed
	sseEventPower    = "power"    // power on/off
	sseEventVolume   = "volume"   // volume level 0–100
	sseEventStation  = "station"  // station name/image resolved
)

// SSE payload types — one per event name.

type sseTrackPayload struct {
	URI        string `json:"uri"`
	Name       string `json:"name"`
	Artists    string `json:"artists"`
	Album      string `json:"album"`
	ShowName   string `json:"show_name,omitempty"`
	DurationMs int    `json:"duration_ms"`
	ImageURL   string `json:"image_url,omitempty"`
}

type ssePlaybackPayload struct {
	Playing    bool `json:"playing"`
	PositionMs int  `json:"position_ms"`
}

type sseStaticPayload struct {
	Playing bool `json:"playing"`
}

type sseDialPayload struct {
	Bucket int    `json:"bucket"`
	Label  string `json:"label"`
}

type sseModePayload struct {
	Mode string `json:"mode"`
}

type ssePowerPayload struct {
	On bool `json:"on"`
}

type sseVolumePayload struct {
	Volume int `json:"volume"`
}

type sseStationPayload struct {
	Name     string `json:"name"`
	ImageURL string `json:"image_url,omitempty"`
}

// sseClient is one connected browser client.
type sseClient struct {
	send chan string
}

// sseBroker manages all connected SSE clients and fans out messages.
type sseBroker struct {
	mu      sync.Mutex
	clients map[*sseClient]struct{}
}

func newSSEBroker() *sseBroker {
	return &sseBroker{clients: make(map[*sseClient]struct{})}
}

func (b *sseBroker) add(c *sseClient) {
	b.mu.Lock()
	b.clients[c] = struct{}{}
	b.mu.Unlock()
}

func (b *sseBroker) remove(c *sseClient) {
	b.mu.Lock()
	delete(b.clients, c)
	b.mu.Unlock()
	close(c.send)
}

// publish encodes payload as JSON and broadcasts a named SSE event to all
// connected clients.
func (b *sseBroker) publish(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		slog.Error("sse: marshal payload", "event", event, "err", err)
		return
	}
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)

	b.mu.Lock()
	defer b.mu.Unlock()
	for c := range b.clients {
		select {
		case c.send <- msg:
		default:
			slog.Warn("sse: client buffer full, dropping event", "event", event)
		}
	}
}

// handleSSE is the HTTP handler for the SSE endpoint. Each connected browser
// gets a persistent response stream. Events are pushed as they arrive on the
// event bus.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Prevent nginx from buffering the stream.
	w.Header().Set("X-Accel-Buffering", "no")

	client := &sseClient{send: make(chan string, 32)}
	s.broker.add(client)
	defer s.broker.remove(client)

	slog.Debug("sse: client connected", "remote", r.RemoteAddr)

	// Send a full state snapshot immediately so the client is up to date
	// without waiting for the next event.
	s.publishSnapshot(client)
	flusher.Flush()

	for {
		select {
		case msg, ok := <-client.send:
			if !ok {
				return
			}
			fmt.Fprint(w, msg)
			flusher.Flush()
		case <-r.Context().Done():
			slog.Debug("sse: client disconnected", "remote", r.RemoteAddr)
			return
		}
	}
}

// publishSnapshot sends the current radioState to a single client as a series
// of individual SSE events — one per event type — so the client renders
// immediately without waiting for the next real event.
func (s *Server) publishSnapshot(c *sseClient) {
	snap := s.state.snapshot(s.cfg.BucketCount)

	send := func(event string, payload any) {
		data, err := json.Marshal(payload)
		if err != nil {
			return
		}
		msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
		select {
		case c.send <- msg:
		default:
		}
	}

	send(sseEventPlayback, ssePlaybackPayload{Playing: snap.Playing})
	send(sseEventStatic, sseStaticPayload{Playing: snap.StaticPlaying})
	send(sseEventDial, sseDialPayload{Bucket: snap.Bucket, Label: snap.BucketLabel})
	send(sseEventMode, sseModePayload{Mode: string(snap.Mode)})
	send(sseEventPower, ssePowerPayload{On: snap.PowerOn})
	send(sseEventVolume, sseVolumePayload{Volume: snap.Volume})
	send(sseEventStation, sseStationPayload{Name: snap.StationName, ImageURL: snap.StationImageURL})
	// Send track last so its artwork is never overwritten by mode/static handlers
	// that call clearArtwork() as part of the snapshot replay.
	send(sseEventTrack, sseTrackPayload{
		Name:     snap.TrackName,
		Artists:  snap.Artists,
		ShowName: snap.ShowName,
		ImageURL: snap.ArtworkURL,
	})
}

// publishClearTrack broadcasts empty track/station/playback/static events so
// connected clients immediately drop stale now-playing info when the station
// changes, rather than waiting for the next KindTrackChanged event.
func (s *Server) publishClearTrack() {
	s.broker.publish(sseEventTrack, sseTrackPayload{})
	s.broker.publish(sseEventStation, sseStationPayload{})
	s.broker.publish(sseEventPlayback, ssePlaybackPayload{Playing: false})
	// Deliberately no sseEventStatic here — let KindStaticStarted/Stopped own
	// that state. Forcing static:false here causes a flash on empty→empty
	// bucket transitions: the NO SIGNAL screen briefly disappears then returns.
}

// runSSEPublisher subscribes to the event bus and translates each event into
// one or more SSE publishes. Runs in its own goroutine.
func (s *Server) runSSEPublisher() {
	ch := s.bus.Subscribe()
	defer s.bus.Unsubscribe(ch)

	for e := range ch {
		switch e.Kind {
		case events.KindTrackChanged:
			var imageURL string
			var imgErr error
			if strings.HasPrefix(e.TrackURI, "spotify:episode:") {
				imageURL, imgErr = s.spotify.GetEpisodeImage(context.Background(), e.TrackURI)
			} else {
				imageURL, imgErr = s.spotify.GetTrackImage(context.Background(), e.TrackURI)
			}
			if imgErr != nil {
				slog.Warn("sse: fetch track image", "err", imgErr)
			}
			s.state.setArtworkURL(imageURL)
			s.broker.publish(sseEventTrack, sseTrackPayload{
				URI:        e.TrackURI,
				Name:       e.TrackName,
				Artists:    e.Artists,
				Album:      e.Album,
				ShowName:   e.ShowName,
				DurationMs: e.DurationMs,
				ImageURL:   imageURL,
			})
		case events.KindPlaybackStateChanged:
			s.broker.publish(sseEventPlayback, ssePlaybackPayload{
				Playing:    e.Playing,
				PositionMs: e.PositionMs,
			})
		case events.KindStaticStarted:
			s.broker.publish(sseEventStatic, sseStaticPayload{Playing: true})
			s.broker.publish(sseEventStation, sseStationPayload{})
		case events.KindStaticStopped:
			s.broker.publish(sseEventStatic, sseStaticPayload{Playing: false})
		case events.KindDialMoved:
			mode := s.state.getMode()
			if mode != events.ModeSpeaker {
				s.publishClearTrack()
			}
			s.broker.publish(sseEventDial, sseDialPayload{
				Bucket: e.Bucket,
				Label:  stationLabel(e.Bucket, s.cfg.BucketCount, string(mode)),
			})
		case events.KindToggleSwitched:
			// Speaker (AFC) mode hands off to Spotify Connect passively — whatever
			// is playing continues, so don't clear now-playing info. For music/podcast
			// a new station is about to load, so clear immediately.
			if e.Mode != events.ModeSpeaker {
				s.publishClearTrack()
			}
			s.broker.publish(sseEventMode, sseModePayload{Mode: string(e.Mode)})
			// Re-emit dial so the frequency label updates.
			snap := s.state.snapshot(s.cfg.BucketCount)
			s.broker.publish(sseEventDial, sseDialPayload{
				Bucket: snap.Bucket,
				Label:  stationLabel(snap.Bucket, s.cfg.BucketCount, string(e.Mode)),
			})
		case events.KindPowerChanged:
			s.broker.publish(sseEventPower, ssePowerPayload{On: e.PowerOn})
			s.publishClearTrack()
		case events.KindVolumeChanged:
			s.broker.publish(sseEventVolume, sseVolumePayload{Volume: e.Volume})
		case events.KindStationChanged:
			s.broker.publish(sseEventStation, sseStationPayload{
				Name:     e.StationName,
				ImageURL: e.StationImageURL,
			})
		}
	}
}
