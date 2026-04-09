package events

import (
	"log/slog"
	"sync"
)

// Kind identifies the type of event on the bus.
type Kind int

const (
	KindTrackChanged        Kind = iota
	KindPlaybackStateChanged     // playing or paused
	KindDialMoved                // dial settled on a new bucket
	KindToggleSwitched           // AM/FM toggle changed mode
	KindStaticStarted            // static audio began playing
	KindStaticStopped            // static audio stopped
	KindPowerChanged             // radio turned on or off via volume knob switch
	KindVolumeChanged            // volume pot position changed (0–100)
	KindStationChanged           // tuned to a new station (name/image resolved)
)

// Mode represents the AM/FM toggle position.
type Mode string

const (
	ModeMusic   Mode = "music"
	ModePodcast Mode = "podcast"
)

// Event carries a single bus event and its associated payload.
// Only the fields relevant to the Kind are populated.
type Event struct {
	Kind Kind

	// KindTrackChanged
	TrackURI   string
	TrackName  string
	Artists    string
	Album      string
	ShowName   string
	DurationMs int

	// KindPlaybackStateChanged
	Playing    bool
	PositionMs int

	// KindDialMoved
	Bucket int

	// KindToggleSwitched
	Mode Mode

	// KindPowerChanged
	PowerOn bool

	// KindVolumeChanged (0–100)
	Volume int

	// KindStationChanged
	StationName     string
	StationImageURL string
}

// Bus is a simple fan-out pub/sub backed by Go channels.
// Publish is non-blocking: if a subscriber's buffer is full the event is
// dropped for that subscriber and a warning is logged.
type Bus struct {
	mu   sync.Mutex
	subs map[int]chan Event
	next int
}

func New() *Bus {
	return &Bus{
		subs: make(map[int]chan Event),
	}
}

// Subscribe returns a channel that receives all future events. Call
// Unsubscribe with the returned channel when done.
func (b *Bus) Subscribe() <-chan Event {
	ch := make(chan Event, 16)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes the subscriber and closes its channel.
func (b *Bus) Unsubscribe(ch <-chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, sub := range b.subs {
		if sub == ch {
			delete(b.subs, id)
			close(sub)
			return
		}
	}
}

// Publish sends e to all current subscribers. Slow subscribers are skipped
// (their event is dropped) to keep the publisher non-blocking.
func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
			slog.Warn("events: subscriber buffer full, dropping event", "kind", e.Kind)
		}
	}
}
