# Zenith Radio — Software Plan

A Go + HTMX server/client application that runs on a Raspberry Pi 4 inside a
retrofitted vintage Zenith radio cabinet. The radio's physical tuning dial (read
via a TMAG5273 Hall effect sensor over I2C) switches between a fixed number of
frequency buckets. Each bucket can be assigned a Spotify playlist — music or
podcast, both are just playlist URIs. Unassigned buckets play audio static to
simulate no signal. The AM/FM toggle switches between two sets of station
mappings (music vs. podcast). Audio playback is handled by librespot; static
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
- **Podcast mode**: Podcast buckets are just Spotify playlist URIs — Spotify's
  "prompted playlists" feature supports podcasts and handles episode refresh on
  a schedule. The AM/FM toggle signals which set of station mappings to use;
  both sets live in the same `stations` table, differentiated by the `mode`
  column. No cron job, no custom episode fetching.
- **Playlist cache invalidation**: Each cached playlist entry stores the Spotify
  `snapshot_id`. On station switch, a lightweight metadata fetch checks whether
  `snapshot_id` has changed; if so, the track list is re-fetched and the cache
  is updated before computing radio time. This handles both music playlist edits
  and prompted playlist refreshes transparently.

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

- Implement subprocess manager: start, capture stderr (forwarded to slog),
  restart on exit with exponential backoff
- Receive playback events via `--onevent`: librespot spawns the `radio` binary
  for each event; the binary detects `PLAYER_EVENT` in env, connects to a Unix
  socket opened by the main process, and forwards event data as JSON. Handled
  events: `track_changed`, `playing`, `paused`, `seeked`, `stopped`,
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
  bundled `noise.mp3` on loop with automatic restart on unexpected exit
- Expose `Start()`, `Stop()`, and `IsPlaying()` methods
- Player is configurable via `STATIC_AUDIO_BIN` (default `ffmpeg`),
  `STATIC_AUDIO_FILE` (default `static/noise.mp3`), and `STATIC_AUDIO_SINK`
  (ALSA device e.g. `hw:0`; empty = auto, suitable for macOS dev)
- `audio.Static` is initialised in `main.go`; `Start()`/`Stop()` are called by
  station-switch logic in Phase 9
- Station-switch logic (Phase 9): if new bucket is unassigned →
  `librespot.Pause()` + `audio.Static.Start()`; if leaving a static bucket →
  `audio.Static.Stop()` + proceed with normal station switch
- `static/noise.mp3` is stored in Git LFS (tracked via `.gitattributes`)

### Phase 6 — Playlist Cache & Snapshot Invalidation

Podcast buckets use Spotify's prompted playlists feature, so no custom episode
fetching or cron job is needed. Both music and podcast stations are plain
playlist URIs. This phase wires up cache invalidation so radio time always
reflects the current playlist contents.

- Add `spotify.GetPlaylistSnapshot(ctx, playlistURI) (string, error)` — a
  lightweight metadata fetch (`?fields=snapshot_id`) to check for changes
  without fetching the full track list
- Implement a file-based playlist cache (JSON, similar to `FileTokenStore`)
  storing `{ snapshot_id, tracks[], total_duration_ms }` per playlist URI;
  migrated to SQLite in Phase 9
- On station switch:
    1. Fetch `snapshot_id` from Spotify
    2. Compare against cached value; if unchanged, use cached tracks
    3. If changed (or no cache entry): fetch full track list, update cache, then
       compute radio time
- Add a `/debug/cache` endpoint to inspect current cache state (removed in
  Phase 10)

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
  accepts a Spotify playlist URI or URL; empty slots labeled "no signal"
- **Podcast config page** (`/config/podcast`): grid of bucket slots; each slot
  accepts a Spotify prompted playlist URI or URL; empty slots labeled "no
  signal"
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
      playlist_uri TEXT,                   -- NULL = unassigned (plays static)
      label TEXT,
      UNIQUE(angle_bucket, mode)
    );

    CREATE TABLE playlist_cache (
      playlist_uri TEXT PRIMARY KEY,
      snapshot_id TEXT NOT NULL,
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
    - `GetStation(bucket, mode) (*Station, error)` — returns nil if unassigned
    - `SetStation(bucket, mode, playlistURI, label) error`
    - `DeleteStation(bucket, mode) error`
    - `ListStations(mode) ([]Station, error)`
    - `GetPlaylistCache(uri) (*PlaylistCache, error)`
    - `SetPlaylistCache(uri, snapshotID, tracks, totalMs) error`
- Wire config UI to store
- Wire station-switch logic: on `DialMoved` or `ToggleSwitched`:
    - If station has no `playlist_uri` (or no station row) → static audio
    - Otherwise → check snapshot, compute radio time, call `spotify.Play`

### Phase 10 — Cleanup & Hardening

Remove temporary scaffolding, replace file-based stubs with proper persistence,
and make the binary production-ready.

- **Remove `/debug/play` endpoint** (`server/handlers.go`, `server/server.go`)
  and the `SPOTIFY_TEST_PLAYLIST` config var (`config/config.go`,
  `.env.example`) — replaced by the real station-switch logic wired up in Phase
  9
- **Migrate `FileTokenStore` to SQLite** (`spotify/auth.go`) — swap
  `FileTokenStore` for a `SQLiteTokenStore` backed by the `tokens` table from
  Phase 9; remove `SPOTIFY_TOKEN_FILE` config var and `spotify-tokens.json`
- **Migrate `FilePlaylistCache` to SQLite** (`spotify/cache.go`) — swap for a
  `SQLitePlaylistCache` backed by the `playlist_cache` table from Phase 9;
  remove `PLAYLIST_CACHE_FILE` config var and `playlist-cache.json`
- **Remove `/debug/cache` endpoint** (`server/debug.go`, `server/server.go`)
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
- **Station assignment UX**: Both music and podcast stations are assigned by
  pasting a Spotify playlist URI or URL. Podcast buckets use Spotify's prompted
  playlists feature for automatic episode refresh. Both managed via `/config` in
  the browser UI — no preset-save dial interaction needed since bucket count is
  fixed.
- **DAC/Amp HAT**: Blocked on confirming driver impedance (4Ω vs 8Ω).
- **librespot version**: v0.8.0. Events delivered via `--onevent` env vars; full
  event reference at https://github.com/librespot-org/librespot/wiki/Events.
- **Static audio file**: A suitable looping static/noise audio file needs to be
  sourced or generated and bundled with the repo. Format should be compatible
  with aplay (WAV) or ffmpeg (anything).

---

## Gold Plating

Features that would be cool but are not critical to the core implementation.
Keep these in mind when making architectural decisions — don't actively design
for them, but don't close the door either if the cost is low.

### AI Voice Interstitials

Between tracks, a TTS system generates a short station ID clip ("You're
listening to 99.1 Rock Radio") and blends it into the audio stream seamlessly.

- **TTS options**: ElevenLabs / Google Cloud TTS / OpenAI TTS for cloud; Piper
  TTS for fully offline on-device generation (runs well on Pi 4)
- **Trigger**: subscribe to `end_of_track` events on the bus; inject
  probabilistically (e.g. 1 in 4 track transitions)
- **Requires ALSA loopback mixing** (see below) — a pause-and-resume approach
  would break the stateless radio time model since wall clock keeps advancing
  while librespot is paused
- Generated clips should be cached to disk since station names rarely change

### Analog Tuning Feel — Static Bleed at Bucket Boundaries

Rather than hard-snapping to a bucket, the audio mix reflects how close the dial
is to the center of a bucket. At dead center: pure music. Drifting toward a
boundary: static bleeds in. At the midpoint between two buckets: mostly static —
like a real analog tuner that needs to be precisely placed.

- **Requires ALSA loopback mixing** (see below)
- **Requires raw angle events from the dial watcher** in addition to the
  settled-bucket events it currently emits — the mixer needs a continuous stream
  of angle readings to update the mix ratio in real time
- Mix ratio curve TBD from experimentation — linear or sigmoid between bucket
  center and boundary

### ALSA Loopback Mixing (shared prerequisite)

Both gold plating features above require routing audio through a virtual ALSA
loopback rather than directly to the hardware output:

```
librespot  →  snd-aloop (virtual device)  ─┐
                                            ├─ Go mixer → hw:0 (real output)
TTS clip / static bleed  ───────────────── ┘
```

- Load `snd-aloop` kernel module on the Pi (add to `/etc/modules`)
- Configure librespot to output to the loopback device rather than `hw:0`
  directly — the `STATIC_AUDIO_SINK` / librespot output config should keep this
  in mind; avoid hardcoding `hw:0` in ways that are hard to reroute later
- The Go mixer process reads PCM frames from the loopback, applies per-frame
  gain curves, and writes to the real output device; a simple custom Go process
  (using `oto` or raw ALSA writes) gives more control than `ffmpeg amix` for
  dynamic ratio changes
- The event bus architecture already supports this: the mixer goroutine
  subscribes to dial angle events and adjusts mix ratios in real time
