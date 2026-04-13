package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"andrewburgess.io/radio/events"
)

const maxRecentEvents = 40

// eventEntry is one row in the debug event log.
type eventEntry struct {
	Time string
	Desc string
}

// radioState holds the last-known value of every event kind published on the
// bus. It is updated by the server's bus subscriber goroutine and read by the
// debug and (later) SSE handlers.
type radioState struct {
	mu              sync.RWMutex
	bucket          int
	mode            events.Mode
	powerOn         bool
	volume          int
	playing         bool
	staticPlaying   bool
	trackName       string
	artists         string
	showName        string
	trackURI        string
	album           string
	artworkURL      string
	stationName     string
	stationImageURL string
	recentEvents    []eventEntry
}

func newRadioState() *radioState {
	return &radioState{
		mode:    events.ModeMusic,
		powerOn: false,
		volume:  0,
	}
}

// stateSnapshot is a lock-free copy of radioState used for template rendering
// and SSE initial snapshots.
type stateSnapshot struct {
	Bucket          int
	Mode            events.Mode
	PowerOn         bool
	Volume          int
	Playing         bool
	StaticPlaying   bool
	TrackName       string
	Artists         string
	ShowName        string
	TrackURI        string
	Album           string
	ArtworkURL      string
	StationName     string
	StationImageURL string
	RecentEvents    []eventEntry
	BucketMaxIndex  int
}

func (rs *radioState) snapshot(bucketCount int) stateSnapshot {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	ev := make([]eventEntry, len(rs.recentEvents))
	copy(ev, rs.recentEvents)
	return stateSnapshot{
		Bucket:          rs.bucket,
		Mode:            rs.mode,
		PowerOn:         rs.powerOn,
		Volume:          rs.volume,
		Playing:         rs.playing,
		StaticPlaying:   rs.staticPlaying,
		TrackName:       rs.trackName,
		Artists:         rs.artists,
		ShowName:        rs.showName,
		TrackURI:        rs.trackURI,
		Album:           rs.album,
		ArtworkURL:      rs.artworkURL,
		StationName:     rs.stationName,
		StationImageURL: rs.stationImageURL,
		RecentEvents:    ev,
		BucketMaxIndex:  bucketCount - 1,
	}
}

func (rs *radioState) update(e events.Event) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	var desc string
	switch e.Kind {
	case events.KindDialMoved:
		rs.bucket = e.Bucket
		desc = fmt.Sprintf("dial_moved: bucket=%d", e.Bucket)
	case events.KindToggleSwitched:
		rs.mode = e.Mode
		rs.artworkURL = ""
		desc = fmt.Sprintf("toggle_switched: mode=%s", e.Mode)
	case events.KindPowerChanged:
		rs.powerOn = e.PowerOn
		desc = fmt.Sprintf("power_changed: on=%v", e.PowerOn)
	case events.KindVolumeChanged:
		rs.volume = e.Volume
		desc = fmt.Sprintf("volume_changed: %d%%", e.Volume)
	case events.KindTrackChanged:
		rs.trackName = e.TrackName
		rs.artists = e.Artists
		rs.showName = e.ShowName
		rs.trackURI = e.TrackURI
		rs.album = e.Album
		rs.artworkURL = ""
		desc = fmt.Sprintf("track_changed: %q — %s", e.TrackName, e.Artists)
	case events.KindPlaybackStateChanged:
		rs.playing = e.Playing
		if e.Playing {
			desc = "playback: playing"
		} else {
			desc = "playback: paused"
		}
	case events.KindStationChanged:
		rs.stationName = e.StationName
		rs.stationImageURL = e.StationImageURL
		desc = fmt.Sprintf("station_changed: %q", e.StationName)
	case events.KindStaticStarted:
		rs.staticPlaying = true
		rs.artworkURL = ""
		rs.stationName = ""
		rs.stationImageURL = ""
		desc = "static: started"
	case events.KindStaticStopped:
		rs.staticPlaying = false
		desc = "static: stopped"
	default:
		return
	}

	entry := eventEntry{Time: time.Now().Format("15:04:05"), Desc: desc}
	rs.recentEvents = append([]eventEntry{entry}, rs.recentEvents...)
	if len(rs.recentEvents) > maxRecentEvents {
		rs.recentEvents = rs.recentEvents[:maxRecentEvents]
	}
}

func (rs *radioState) setArtworkURL(url string) {
	rs.mu.Lock()
	rs.artworkURL = url
	rs.mu.Unlock()
}

// handleDebug renders the full debug panel page.
func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	s.render(w, "debug", "base", s.state.snapshot(s.cfg.BucketCount))
}

// handleDebugState returns the polled state fragment (includes OOB event log).
func (s *Server) handleDebugState(w http.ResponseWriter, r *http.Request) {
	s.render(w, "debug", "debug-state-poll", s.state.snapshot(s.cfg.BucketCount))
}

// handleDebugSimulate fires a synthetic event onto the bus and returns the
// updated state fragment so HTMX can swap it in immediately.
func (s *Server) handleDebugSimulate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	action := r.FormValue("action")
	switch action {
	case "dial":
		bucket, _ := strconv.Atoi(r.FormValue("bucket"))
		if bucket < 0 {
			bucket = 0
		}
		if bucket >= s.cfg.BucketCount {
			bucket = s.cfg.BucketCount - 1
		}
		s.bus.Publish(events.Event{Kind: events.KindDialMoved, Bucket: bucket})

	case "toggle":
		mode := events.Mode(r.FormValue("mode"))
		if mode != events.ModeMusic && mode != events.ModePodcast {
			mode = events.ModeMusic
		}
		s.bus.Publish(events.Event{Kind: events.KindToggleSwitched, Mode: mode})

	case "power":
		on := r.FormValue("power") == "on"
		s.bus.Publish(events.Event{Kind: events.KindPowerChanged, PowerOn: on})

	case "volume":
		vol, _ := strconv.Atoi(r.FormValue("volume"))
		if vol < 0 {
			vol = 0
		}
		if vol > 100 {
			vol = 100
		}
		s.bus.Publish(events.Event{Kind: events.KindVolumeChanged, Volume: vol})

	default:
		slog.Warn("debug/simulate: unknown action", "action", action)
	}

	// Small yield so the bus subscriber goroutine can process the event before
	// we read the state back for the response.
	time.Sleep(10 * time.Millisecond)
	s.render(w, "debug", "debug-state-poll", s.state.snapshot(s.cfg.BucketCount))
}
