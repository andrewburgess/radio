package librespot

import "testing"

func TestParseRawEvent_TrackChanged(t *testing.T) {
	raw := map[string]string{
		"PLAYER_EVENT": "track_changed",
		"TRACK_ID":     "abc123",
		"URI":          "spotify:track:abc123",
		"NAME":         "Some Track",
		"DURATION_MS":  "240000",
		"ITEM_TYPE":    "Track",
		"ARTISTS":      "Artist A",
		"ALBUM":        "Some Album",
	}
	evt, ok := parseRawEvent(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if evt.Type != EventTrackChanged {
		t.Errorf("Type = %v, want EventTrackChanged", evt.Type)
	}
	if evt.URI != "spotify:track:abc123" {
		t.Errorf("URI = %q, want %q", evt.URI, "spotify:track:abc123")
	}
	if evt.Name != "Some Track" {
		t.Errorf("Name = %q, want %q", evt.Name, "Some Track")
	}
	if evt.DurationMs != 240000 {
		t.Errorf("DurationMs = %d, want 240000", evt.DurationMs)
	}
	if evt.ItemType != ItemTypeTrack {
		t.Errorf("ItemType = %q, want %q", evt.ItemType, ItemTypeTrack)
	}
}

func TestParseRawEvent_Episode(t *testing.T) {
	raw := map[string]string{
		"PLAYER_EVENT": "track_changed",
		"URI":          "spotify:episode:xyz789",
		"NAME":         "Episode 42",
		"DURATION_MS":  "3600000",
		"ITEM_TYPE":    "Episode",
		"SHOW_NAME":    "Cool Podcast",
	}
	evt, ok := parseRawEvent(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if evt.ItemType != ItemTypeEpisode {
		t.Errorf("ItemType = %q, want %q", evt.ItemType, ItemTypeEpisode)
	}
	if evt.ShowName != "Cool Podcast" {
		t.Errorf("ShowName = %q, want %q", evt.ShowName, "Cool Podcast")
	}
	if evt.DurationMs != 3600000 {
		t.Errorf("DurationMs = %d, want 3600000", evt.DurationMs)
	}
}

func TestParseRawEvent_Playing(t *testing.T) {
	raw := map[string]string{
		"PLAYER_EVENT": "playing",
		"TRACK_ID":     "abc123",
		"POSITION_MS":  "12500",
	}
	evt, ok := parseRawEvent(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if evt.Type != EventPlaying {
		t.Errorf("Type = %v, want EventPlaying", evt.Type)
	}
	if evt.PositionMs != 12500 {
		t.Errorf("PositionMs = %d, want 12500", evt.PositionMs)
	}
}

func TestParseRawEvent_Paused(t *testing.T) {
	raw := map[string]string{
		"PLAYER_EVENT": "paused",
		"TRACK_ID":     "abc123",
		"POSITION_MS":  "30000",
	}
	evt, ok := parseRawEvent(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if evt.Type != EventPaused {
		t.Errorf("Type = %v, want EventPaused", evt.Type)
	}
}

func TestParseRawEvent_Stopped(t *testing.T) {
	raw := map[string]string{
		"PLAYER_EVENT": "stopped",
		"TRACK_ID":     "abc123",
	}
	evt, ok := parseRawEvent(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if evt.Type != EventStopped {
		t.Errorf("Type = %v, want EventStopped", evt.Type)
	}
}

func TestParseRawEvent_VolumeChanged(t *testing.T) {
	raw := map[string]string{
		"PLAYER_EVENT": "volume_changed",
		"VOLUME":       "32768",
	}
	evt, ok := parseRawEvent(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if evt.Type != EventVolumeChanged {
		t.Errorf("Type = %v, want EventVolumeChanged", evt.Type)
	}
	if evt.Volume != 32768 {
		t.Errorf("Volume = %d, want 32768", evt.Volume)
	}
}

func TestParseRawEvent_SessionConnected(t *testing.T) {
	raw := map[string]string{
		"PLAYER_EVENT":  "session_connected",
		"USER_NAME":     "testuser",
		"CONNECTION_ID": "conn-abc",
	}
	evt, ok := parseRawEvent(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if evt.Type != EventSessionConnected {
		t.Errorf("Type = %v, want EventSessionConnected", evt.Type)
	}
	if evt.UserName != "testuser" {
		t.Errorf("UserName = %q, want %q", evt.UserName, "testuser")
	}
	if evt.ConnectionID != "conn-abc" {
		t.Errorf("ConnectionID = %q, want %q", evt.ConnectionID, "conn-abc")
	}
}

func TestParseRawEvent_Unknown(t *testing.T) {
	raw := map[string]string{
		"PLAYER_EVENT": "preloading",
		"TRACK_ID":     "abc123",
	}
	_, ok := parseRawEvent(raw)
	if ok {
		t.Error("expected ok=false for unhandled event type")
	}
}

func TestParseRawEvent_MissingFields(t *testing.T) {
	// Fields absent from the map should parse to zero values, not panic.
	raw := map[string]string{
		"PLAYER_EVENT": "playing",
	}
	evt, ok := parseRawEvent(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if evt.PositionMs != 0 {
		t.Errorf("PositionMs = %d, want 0", evt.PositionMs)
	}
}

func TestParseRawEvent_InvalidInt(t *testing.T) {
	// Non-numeric values should parse to 0 without panicking.
	raw := map[string]string{
		"PLAYER_EVENT": "playing",
		"POSITION_MS":  "not-a-number",
	}
	evt, ok := parseRawEvent(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if evt.PositionMs != 0 {
		t.Errorf("PositionMs = %d, want 0", evt.PositionMs)
	}
}
