# Zenith Radio — Claude Working Notes

## Documentation Maintenance

Whenever a design decision is made, an approach changes, or new env vars /
conventions are introduced, update **all three** of these files before moving
on:

- **CLAUDE.md** — conventions, env vars, constraints, dev notes
- **PLAN.md** — architecture decisions, phase descriptions, open questions
- **README.md** — user-facing build and setup instructions

Do not leave them out of sync.

---

## Project Summary

A Go + HTMX application running on a Raspberry Pi 4 inside a retrofitted Zenith
radio cabinet. A physical tuning dial (TMAG5273 Hall effect sensor over I2C)
selects between station buckets; an AM/FM toggle switches between music and
podcast mode. Audio is played via a managed librespot subprocess. Unassigned
buckets play looping static noise. See PLAN.md for full architecture.

## Build Phases

Complete phases in order. Each phase must be independently functional before
starting the next.

1. **Phase 1** — Project scaffold (HTTP server, config loading)
2. **Phase 2** — librespot subprocess + Spotify Web API client + radio time
3. **Phase 3** — Internal event bus (pub/sub via Go channels)
4. **Phase 4** — Hardware watchers (TMAG5273 I2C dial, AM/FM GPIO toggle)
5. **Phase 5** — Static audio subprocess (ffmpeg/aplay on loop)
6. **Phase 6** — Podcast scheduler (cron, episode fetch, round-robin interleave)
7. **Phase 7** — SSE endpoint (real-time push to browser clients)
8. **Phase 8** — HTMX UI (now-playing, music config, podcast config)
9. **Phase 9** — SQLite persistence (schema, store layer, wiring)
10. **Phase 10** — Cleanup & hardening (remove debug scaffolding, migrate
    FileTokenStore to SQLite, graceful shutdown)

## Code Conventions

- **Language**: Go. Target the latest stable Go version.
- **Module path**: `andrewburgess.io/radio` (initialize with `go mod init`)
- **Error handling**: return errors up the call stack; wrap with
  `fmt.Errorf("context: %w", err)`
- **Logging**: use `log/slog` structured logging throughout; default level Info
- **No CGO**: use `modernc.org/sqlite` (pure Go SQLite). Do not use
  mattn/go-sqlite3.
- **No framework**: `net/http` stdlib only for HTTP.
- **Concurrency**: use channels for inter-goroutine communication; avoid shared
  mutable state where possible; protect shared state with `sync.Mutex` when
  necessary.
- **Subprocess management**: use `os/exec`; always capture stderr, always handle
  process death and restart.
- **Config**: environment variables only, loaded at startup via a `config`
  package. No config files; no flags.
- **Tests**: write unit tests for pure logic (radio time calculation,
  round-robin interleave, angle→bucket mapping). Hardware-dependent code does
  not need tests.
- **Build tags**: use the `pi` build tag to gate real hardware implementations.
  Every file in `hardware/` that uses `periph.io` or GPIO/I2C must have
  `//go:build pi` at the top. Each must have a corresponding `_mock.go` file
  with `//go:build !pi` that satisfies the same interface with no-op or
  simulated behaviour. Build for the Pi with `go build -tags pi`; dev builds use
  `go build` (no tag) and get the mocks automatically.
- **Pi dependencies**: `periph.io/x/periph` (and sub-packages) are only pulled
  in when building with `-tags pi`. Run `go get periph.io/x/periph` on the Pi
  before the first `go build -tags pi`.

## Key Dependencies

| Package                | Purpose                              |
| ---------------------- | ------------------------------------ |
| `modernc.org/sqlite`   | Pure-Go SQLite driver                |
| `periph.io/x/periph`   | GPIO and I2C on Raspberry Pi         |
| Spotify Web API (REST) | Playback control, metadata, episodes |
| HTMX (CDN or vendored) | Frontend interactivity               |

No ORM. Write raw SQL.

## Directory Layout

Follow the structure in PLAN.md exactly:

```
radio/
├── main.go
├── go.mod / go.sum
├── static/noise.mp3
├── config/config.go
├── hardware/dial.go, toggle.go
├── librespot/process.go
├── audio/static.go
├── spotify/auth.go, client.go
├── podcast/scheduler.go
├── events/bus.go
├── server/server.go, sse.go, handlers.go, templates/
└── store/store.go
```

Do not create files outside this structure without a good reason.

## Important Constraints

- **No CGO**: `modernc.org/sqlite` only — the binary must cross-compile for
  `GOARCH=arm64 GOOS=linux` from a development machine.
- **No runtime config changes**: bucket count is fixed at startup from env;
  never allow it to change while the server is running.
- **Radio time is stateless**: current playback position is always derived from
  `unixNow % totalPlaylistDurationMs` — never stored. Recalculate on every
  station switch.
- **Podcast no-interrupt rule**: the cron job must skip refresh entirely if the
  toggle is currently in podcast mode. Check mode before doing any API calls.
- **Static audio fallback**: an empty podcast pseudo-playlist is treated
  identically to an unassigned bucket — play static noise.
- **librespot is the Spotify device**: Go controls it via the Web API; Go does
  not implement the Spotify Connect protocol itself.

## Environment Variables (config package)

| Variable                | Description                                               | Default            |
| ----------------------- | --------------------------------------------------------- | ------------------ |
| `PORT`                  | HTTP listen port                                          | `8080`             |
| `DB_PATH`               | Path to SQLite database file                              | `radio.db`         |
| `LIBRESPOT_BIN`         | Path to librespot binary                                  | `librespot`        |
| `LIBRESPOT_DEVICE_NAME` | Spotify Connect device name                               | `Zenith Radio`     |
| `LIBRESPOT_CACHE_DIR`   | Directory for librespot credential/file cache             | `librespot-cache`  |
| `BUCKET_COUNT`          | Number of dial stations                                   | `12`               |
| `PODCAST_WINDOW_DAYS`   | Episode recency window for podcast cron                   | `14`               |
| `PODCAST_CRON_INTERVAL` | How often the podcast cron runs (e.g. `6h`)               | `6h`               |
| `STATIC_AUDIO_BIN`      | Binary for looping static audio (`ffmpeg`/`aplay`)        | `ffmpeg`           |
| `STATIC_AUDIO_FILE`     | Path to the static noise audio file                       | `static/noise.mp3` |
| `STATIC_AUDIO_SINK`     | ALSA output device for ffmpeg (e.g. `hw:0`); empty = auto | `""`               |
| `SPOTIFY_CLIENT_ID`     | Spotify app client ID                                     | required           |
| `SPOTIFY_CLIENT_SECRET` | Spotify app client secret                                 | required           |
| `SPOTIFY_REDIRECT_URI`  | OAuth redirect URI for auth code flow                     | required           |

## SQLite Schema

Defined in full in PLAN.md (Phase 9). Tables: `stations`, `music_stations`,
`podcast_shows`, `podcast_playlists`, `playlist_cache`, `tokens`.

Apply schema via embedded SQL in `store/store.go` on first open
(`CREATE TABLE IF NOT EXISTS`).

## Development Notes

- The Pi is the deployment target but all non-hardware code should compile and
  run on a dev machine (Linux/macOS/Windows) for faster iteration.
- Hardware packages (`hardware/`, TMAG5273 reads, GPIO toggle) will only work on
  the Pi; guard with a build tag or a mock interface when running locally.
- Pre-built librespot binaries are in `bin/` (`librespot-linux-arm64` for the
  Pi, `librespot-darwin-amd64` for macOS dev). Set `LIBRESPOT_BIN` to the
  appropriate path.
- Static noise file (`static/noise.mp3`) must be sourced and committed before
  Phase 5 is functional.
