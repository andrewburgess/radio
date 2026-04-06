# Zenith Radio — Software Plan

A Go + HTMX server/client application that runs on a Raspberry Pi 4 inside a
retrofitted vintage Zenith radio cabinet. The radio's physical tuning dial (read
via a TMAG5273 Hall effect sensor over I2C) switches between a fixed number of
frequency buckets. Each bucket can be assigned a Spotify playlist (music mode)
or a set of podcast shows (podcast mode). Unassigned buckets play audio static
to simulate no signal. The AM/FM toggle switches between music and podcast modes
— both sourced from Spotify. Audio playback is handled by librespot; static
audio is played via a separate ffmpeg/aplay process.

---

## Architecture Overview

```
┌─────────────────────────────────────────┐
│              Pi (Go server)             │
│                                         │
│  ┌─────────┐   ┌─────────────────────┐  │
│  │  GPIO   │   │  librespot process  │  │
│  │ watcher │   │  (managed subprocess│  │
│  │goroutine│   │   via exec.Cmd)     │  │
│  └────┬────┘   └──────────┬──────────┘  │
│       │                   │             │
│  ┌────▼───────────────────▼──────────┐  │
│  │         Event bus (channels)      │  │
│  └────────────────┬──────────────────┘  │
│                   │                     │
│  ┌────────────────▼──────────────────┐  │
│  │     HTTP server (Go net/http)     │  │
│  │  - HTMX routes (config UI)        │  │
│  │  - SSE endpoint (live state)      │  │
│  │  - Spotify Web API calls          │  │
│  └───────────────────────────────────┘  │
└─────────────────────────────────────────┘
          ↕  browser on local network
┌─────────────────────────────────────────┐
│         Client (HTMX + minimal JS)      │
│  - Playlist ↔ frequency mapping UI      │
│  - Now playing display (SSE-driven)     │
│  - GPIO state panel (SSE-driven)        │
└─────────────────────────────────────────┘
```

### Key Design Decisions

- **librespot as a subprocess**: Go spawns, monitors, and restarts librespot.
  Playback events are received via librespot's `--onevent` mechanism: librespot
  spawns the `radio` binary for each event with event data in env vars. The
  binary detects this via `PLAYER_EVENT` being set, connects to a Unix domain
  socket opened by the main process, and forwards the event as JSON.
- **Spotify Web API**: Used for playback control (play, queue, set playlist
  context) and metadata. librespot registers the Pi as the target device. Auth
  uses the Authorization Code flow — a client ID and secret are already
  available.
- **SSE over WebSockets**: The client needs real-time updates (now playing, GPIO
  state) but all data flows server → client only. SSE is simpler and HTMX
  supports it natively.
- **Config persistence**: SQLite via `modernc.org/sqlite` (pure Go, no CGO).
  Stores the mapping of dial positions (station index) to Spotify playlist URIs
  and mode (music vs. podcast).
- **GPIO / I2C**: `periph.io` for GPIO. The TMAG5273 sensor communicates over
  I2C; reads absolute angle for dial position. The AM/FM toggle reads as a
  simple GPIO input.
- **Station count**: Fixed number of buckets, configurable at startup (e.g. 12).
  The dial's full angular range is divided equally. Bucket count is set in
  config and does not change at runtime. Unassigned buckets play static.
- **Radio time (stateless playback position)**: Stations behave like real radio
  — they play continuously whether you're listening or not. When switching to a
  station, the server calculates where in the playlist you _would_ be based on
  wall-clock time, using a modulo over cumulative track durations. This requires
  fetching the full playlist's track list and durations from the Spotify Web API
  at station-switch time (or on a cache refresh). No playback position state is
  stored; the current position is always derived from
  `now % total_playlist_duration`.
- **Static audio (no-signal buckets)**: When the dial lands on an unassigned
  bucket, librespot is paused and a local static audio file is played in a loop
  via a managed ffmpeg (or aplay) subprocess. When leaving a static bucket, the
  static process is killed and librespot resumes for the new station. A static
  audio file must be bundled with the application.
- **Podcast mode — show buckets**: Each podcast bucket maps to a curated
  collection of Spotify show URIs (e.g. "technology" bucket = 3–4 tech shows). A
  background cron job fetches recent episodes from each show in the collection
  (episodes published within a configurable window, e.g. last 14 days) and
  assembles them into a pseudo-playlist ordered by round-robin interleaving
  across shows. Radio time applies to this pseudo-playlist identically to music
  mode.
- **Podcast cron — no-interrupt rule**: The cron job checks whether the radio is
  currently in podcast mode before refreshing any station's pseudo-playlist. If
  podcast mode is active, the job reschedules itself and skips the refresh
  entirely. Refresh runs only when the toggle is in music mode. This is the
  simplest safe boundary — no per-station tracking needed.
- **Podcast mode**: The AM/FM toggle signals which set of station mappings to
  use — music stations vs. podcast stations. Both sets live in the same
  `stations` table, differentiated by the `mode` column.

---

## Tech Stack

| Concern            | Choice                          | Notes                                                                      |
| ------------------ | ------------------------------- | -------------------------------------------------------------------------- |
| Language           | Go                              | Single static binary, low memory, good fit for HTMX                        |
| Web framework      | `net/http` (stdlib)             | No framework needed at this scale                                          |
| Templating         | `html/template` (stdlib)        | Pairs naturally with HTMX                                                  |
| Frontend           | HTMX                            | Minimal JS, SSE support built in                                           |
| GPIO / I2C         | `periph.io`                     | Most mature Go Pi library                                                  |
| Database           | SQLite via `modernc.org/sqlite` | Pure Go, no CGO required                                                   |
| Spotify playback   | librespot                       | Managed subprocess                                                         |
| Spotify control    | Spotify Web API                 | REST calls from Go using Authorization Code tokens                         |
| Static audio       | ffmpeg or aplay                 | Managed subprocess, plays local static file on loop for unassigned buckets |
| Podcast scheduling | Go `time.AfterFunc` / ticker    | Built-in, no external cron daemon needed                                   |

---

## Project Structure

```
radio/
├── main.go                  # Entry point, wires everything together
├── go.mod
├── go.sum
├── static/
│   └── noise.mp3            # Bundled static audio file for no-signal buckets
│
├── config/
│   └── config.go            # App config (ports, secrets, paths, bucket count, podcast window) from env
│
├── hardware/
│   ├── dial.go              # TMAG5273 I2C reads, angle → bucket mapping
│   └── toggle.go            # AM/FM GPIO toggle
│
├── librespot/
│   └── process.go           # Subprocess management, stdout event parsing, restart logic
│
├── audio/
│   └── static.go            # ffmpeg/aplay subprocess: play static file on loop, stop on demand
│
├── spotify/
│   ├── auth.go              # Authorization Code flow, token storage, refresh
│   └── client.go            # Web API calls (play, pause, skip, set context URI, fetch episodes)
│
├── podcast/
│   └── scheduler.go         # Cron logic: fetch recent episodes, round-robin interleave, build pseudo-playlist
│                            # Respects no-interrupt rule: skips refresh if podcast mode is active
│
├── events/
│   └── bus.go               # Internal pub/sub using Go channels
│
├── server/
│   ├── server.go            # HTTP server setup, route registration
│   ├── sse.go               # SSE endpoint, fan-out to connected clients
│   ├── handlers.go          # HTMX route handlers (config pages, actions)
│   └── templates/
│       ├── base.html
│       ├── index.html       # Now playing + GPIO state
│       └── config.html      # Frequency → playlist/show-set mapping UI
│
└── store/
    └── store.go             # SQLite: station config CRUD, playlist cache, podcast pseudo-playlists
```

---

## Build Sequence

Work through these phases in order. Each phase should be independently
functional before starting the next.

### Phase 1 — Project Scaffold

- Initialize Go module (`go mod init`)
- Implement basic `net/http` server on a configurable port
- Serve a placeholder HTML page confirming the server is reachable from the
  network
- Set up config loading (port, DB path, librespot binary path, bucket count,
  podcast episode window in days) from environment variables

### Phase 2 — librespot Integration

- Implement subprocess manager: start, capture stderr (forwarded to slog), restart on exit with exponential backoff
- Receive playback events via `--onevent`: librespot spawns the `radio` binary
  for each event; the binary detects `PLAYER_EVENT` in env, connects to a Unix
  socket opened by the main process, and forwards event data as JSON.
  Handled events: `track_changed`, `playing`, `paused`, `seeked`, `stopped`,
  `end_of_track`, `volume_changed`, `session_connected`, `session_disconnected`
- Implement Spotify Web API client:
    - Authorization Code flow (browser-based, one-time setup)
    - Token storage to disk and refresh logic
    - Methods: `Play(contextURI)`, `Pause()`, `Skip()`, `GetCurrentTrack()`,
      `GetPlaylistTracks(playlistURI)` (returns track list with durations for
      radio time calculation), `GetShowEpisodes(showURI, since time.Time)` (for
      podcast cron)
- Implement radio time calculation:
    - On station switch, fetch playlist tracks + durations (cache aggressively —
      invalidate only when playlist is reassigned)
    - Compute `offset = unixNow % totalPlaylistDurationMs`
    - Walk track list to find which track and position within that track the
      offset falls on
    - Call Spotify Web API `play` with `offset: { position: trackIndex }` and
      `position_ms`
- Verify end-to-end: Go starts librespot → Pi appears as Spotify device → Go can
  send play commands via Web API

### Phase 3 — Event Bus

- Implement a simple pub/sub using Go channels
- Define event types: `TrackChanged`, `PlaybackStateChanged`, `DialMoved`,
  `ToggleSwitched`, `StaticStarted`, `StaticStopped`
- Wire librespot event socket and hardware watchers as publishers
- SSE handler and station-switch logic as subscribers

### Phase 4 — Hardware Watchers

- Implement TMAG5273 I2C reads via `periph.io`
    - Read raw angle, divide range into N equal buckets (N from config), emit
      `DialMoved` events
    - Station switch logic: only emit a switch event after the dial has settled
      (debounce)
- Implement AM/FM toggle GPIO read, emit `ToggleSwitched` events
- Test in isolation (log events to stdout) before wiring to playback

### Phase 5 — Static Audio

- Implement `audio.Static`: managed ffmpeg (or aplay) subprocess that plays the
  bundled `noise.mp3` on loop
- Expose `Start()` and `Stop()` methods; `Stop()` kills the subprocess cleanly
- Station-switch logic: if new bucket is unassigned → `librespot.Pause()` +
  `audio.Static.Start()`; if leaving a static bucket → `audio.Static.Stop()` +
  proceed with normal station switch
- Bundle a suitable static/noise audio file with the repo

### Phase 6 — Podcast Scheduler

- Implement `podcast.Scheduler`: a background goroutine that fires on a
  configurable interval (e.g. every 6 hours)
- On each tick:
    - Check current mode; if podcast mode is active, log skip reason and
      reschedule — do not refresh
    - For each podcast bucket, fetch episodes from each assigned show URI
      published within the configured window (e.g. last 14 days) using
      `spotify.GetShowEpisodes`
    - Interleave episodes round-robin across shows in the bucket (show A ep 1,
      show B ep 1, show C ep 1, show A ep 2, ...)
    - Write resulting pseudo-playlist (ordered list of episode URIs + durations)
      to `podcast_playlists` table
    - Invalidate playlist cache for affected buckets
- Pseudo-playlists are used identically to music playlists for radio time
  calculation

### Phase 7 — SSE Endpoint

- Implement SSE handler that fans out to all connected browser clients
- Subscribe to event bus; push relevant state on each event:
    - Now playing (track name, artist, album art URL, show name if podcast)
    - Current bucket index, mode, and playlist/show-set URI
    - GPIO state (dial angle, toggle position)
    - Static state (whether static audio is playing)
- Handle client connect/disconnect cleanly

### Phase 8 — HTMX UI

- **Now playing page** (`/`): displays current track info and GPIO state,
  updated via SSE; shows "no signal" state when static is playing
- **Music config page** (`/config/music`): grid of bucket slots; each slot
  accepts a Spotify playlist URI or link; empty slots labeled "no signal"
- **Podcast config page** (`/config/podcast`): grid of bucket slots; each slot
  manages a list of Spotify show URIs (add/remove shows); displays last refresh
  time and episode count per bucket
- Use HTMX `hx-get`/`hx-post` for all interactions — no full page reloads
- Use HTMX SSE extension for live updates on the now-playing page
- Keep CSS minimal — functional over styled, dark theme preferred

### Phase 9 — Persistence

- SQLite schema:

    ```sql
    CREATE TABLE stations (
      id INTEGER PRIMARY KEY,
      angle_bucket INTEGER NOT NULL,
      mode TEXT NOT NULL CHECK(mode IN ('music', 'podcast')),
      label TEXT,
      UNIQUE(angle_bucket, mode)
    );

    -- Music mode: one playlist URI per station
    CREATE TABLE music_stations (
      station_id INTEGER PRIMARY KEY REFERENCES stations(id),
      playlist_uri TEXT NOT NULL
    );

    -- Podcast mode: one or more show URIs per station
    CREATE TABLE podcast_shows (
      id INTEGER PRIMARY KEY,
      station_id INTEGER NOT NULL REFERENCES stations(id),
      show_uri TEXT NOT NULL,
      display_name TEXT
    );

    -- Materialized pseudo-playlist built by cron; rebuilt on each refresh
    CREATE TABLE podcast_playlists (
      station_id INTEGER NOT NULL REFERENCES stations(id),
      position INTEGER NOT NULL,           -- ordering index
      episode_uri TEXT NOT NULL,
      duration_ms INTEGER NOT NULL,
      PRIMARY KEY (station_id, position)
    );

    CREATE TABLE playlist_cache (
      playlist_uri TEXT PRIMARY KEY,
      tracks_json TEXT NOT NULL,           -- JSON array of {uri, duration_ms}
      total_duration_ms INTEGER NOT NULL,
      cached_at INTEGER NOT NULL
    );

    CREATE TABLE tokens (
      id INTEGER PRIMARY KEY CHECK(id = 1),
      access_token TEXT NOT NULL,
      refresh_token TEXT NOT NULL,
      expires_at INTEGER NOT NULL
    );
    ```

- Implement store layer:
    - `GetStation(bucket, mode)`, `SetStation(...)`,
      `DeleteStation(bucket, mode)`, `ListStations(mode)`
    - `SetMusicPlaylist(stationID, uri)`, `GetMusicPlaylist(stationID)`
    - `AddPodcastShow(stationID, showURI)`, `RemovePodcastShow(id)`,
      `ListPodcastShows(stationID)`
    - `SetPodcastPlaylist(stationID, episodes)`, `GetPodcastPlaylist(stationID)`
- Wire config UI to store
- Wire station-switch logic: on `DialMoved` or `ToggleSwitched`:
    - If bucket is unassigned → static audio
    - If music mode → look up `music_stations`, compute radio time, call
      `spotify.PlayAt`
    - If podcast mode → look up `podcast_playlists`, compute radio time, call
      `spotify.PlayAt`

### Phase 10 — Cleanup & Hardening

Remove temporary scaffolding, replace file-based stubs with proper persistence,
and make the binary production-ready.

- **Remove `/debug/play` endpoint** (`server/handlers.go`, `server/server.go`)
  and the `SPOTIFY_TEST_PLAYLIST` config var (`config/config.go`, `.env.example`)
  — replaced by the real station-switch logic wired up in Phase 9
- **Migrate `FileTokenStore` to SQLite** (`spotify/auth.go`) — swap
  `FileTokenStore` for a `SQLiteTokenStore` backed by the `tokens` table from
  Phase 9; remove `SPOTIFY_TOKEN_FILE` config var and `spotify-tokens.json`
- **Promote the Spotify "not authorized" warning to a redirect** (`main.go`) —
  instead of logging a warning and continuing, redirect all non-auth HTTP
  requests to `/auth` until a token is present
- **Review logging levels** — audit `slog.Info` calls that are too noisy for
  normal operation (e.g. per-event librespot lines) and demote to `Debug`
- **Graceful HTTP shutdown** (`server/server.go`) — replace
  `http.ListenAndServe` with `http.Server` + `Shutdown(ctx)` so in-flight
  requests drain cleanly on SIGINT/SIGTERM

---

## Hardware Reference

| Component                  | Interface         | Notes                                                                                                                                           |
| -------------------------- | ----------------- | ----------------------------------------------------------------------------------------------------------------------------------------------- |
| TMAG5273                   | I2C               | Absolute angle sensor. Magnet epoxied to tuning shaft. Sensing range 0.5–3mm. Station count TBD from experimentation.                           |
| AM/FM toggle               | GPIO (digital in) | Switches between music and podcast station sets. Both modes use Spotify; toggle determines which `mode` row is looked up in the stations table. |
| Raspberry Pi 4             | —                 | Runs always-on. Boot time optimization is not a priority.                                                                                       |
| DAC/Amp HAT                | I2S               | TBD pending driver impedance check (4Ω vs 8Ω). HiFiBerry MiniAmp or Pimoroni Audio Amp SHIM candidate.                                          |
| Salvaged full-range driver | —                 | Impedance TBD                                                                                                                                   |

---

## Open Questions / Deferred Decisions

- **Station count (bucket count)**: The fixed number of buckets is configurable
  at startup. The right number depends on physical testing with the TMAG5273 —
  how many distinct positions the dial can reliably land on without ambiguity.
  Start with a conservative number (e.g. 8–12) and tune from there.
- **Station assignment UX**: Music stations are assigned by pasting a Spotify
  playlist URI. Podcast stations are assigned by adding show URIs to a bucket's
  collection. Both managed via `/config` in the browser UI — no preset-save dial
  interaction needed since bucket count is fixed.
- **DAC/Amp HAT**: Blocked on confirming driver impedance (4Ω vs 8Ω).
- **librespot version**: v0.8.0. Events delivered via `--onevent` env vars;
  full event reference at https://github.com/librespot-org/librespot/wiki/Events.
- **Static audio file**: A suitable looping static/noise audio file needs to be
  sourced or generated and bundled with the repo. Format should be compatible
  with aplay (WAV) or ffmpeg (anything).
- **Podcast cron interval**: Configurable; 6 hours is a reasonable default.
  Should also run once at startup to ensure pseudo-playlists are populated
  before the radio is used.
- **Empty podcast bucket behavior**: If a podcast bucket has shows assigned but
  the cron finds no episodes within the recency window, the pseudo-playlist is
  left empty. The station-switch logic treats an empty pseudo-playlist
  identically to an unassigned bucket — falls back to static audio.
