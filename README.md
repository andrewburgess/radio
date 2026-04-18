# Zenith Radio

A Go + HTMX application running on a Raspberry Pi 4 inside a retrofitted Zenith
radio cabinet. A physical tuning dial selects between station buckets; an AM/FM
toggle switches between music and podcast mode. Audio is played via a managed
librespot subprocess. See [PLAN.md](PLAN.md) for the full architecture.

---

## Quick Install (Recommended)

The install script is the fastest path to a running radio. It builds the binary
on the Pi, copies everything to `/opt/radio/`, and registers a systemd service
that starts automatically on boot.

**Prerequisites:** run the one-time dependency install first (see below).

```sh
./scripts/install.sh
systemctl --user start radio
```

What the script does:

- Builds `radio` with `CGO_ENABLED=1 go build -tags pi`
- Copies the binary, librespot, static audio files, and interstitials to
  `/opt/radio/`
- Copies your existing `.env` to `/opt/radio/.env` (or `.env.example` on first
  install)
- Installs and enables `radio.service` via systemd

After the first install, edit `/opt/radio/.env` with your Spotify credentials
before starting the service. Subsequent reinstalls never overwrite `.env`.

```sh
systemctl --user restart radio              # after a reinstall
systemctl --user status radio
journalctl --user -u radio -f              # follow logs
journalctl --user -u radio -f --output cat # plain output, easier to read
```

The service runs as your user and is granted `CAP_NET_BIND_SERVICE` so it can
listen on port 80 without `sudo`. Set `PORT=80` in `/opt/radio/.env`.

---

## Manual Setup

### 1. Install build dependencies (once)

```sh
sudo apt-get install -y golang libasound2-dev libasound2-plugins pipewire-alsa pulseaudio-utils
```

| Package              | Purpose                                                                            |
| -------------------- | ---------------------------------------------------------------------------------- |
| `libasound2-dev`     | ALSA headers required by `oto` at build time                                       |
| `libasound2-plugins` | Adds the `pulse` PCM device to ALSA, so librespot can route audio through PipeWire |
| `pipewire-alsa`      | Makes PipeWire the default ALSA device for all apps (enables per-stream mixing)    |
| `pulseaudio-utils`   | Provides `pactl` for inspecting and controlling PipeWire streams                   |

Pi OS Bookworm ships PipeWire pre-installed. `pipewire-alsa` and
`libasound2-plugins` connect ALSA-native apps (librespot, oto) into PipeWire's
mixer so their audio streams can be controlled independently — required for
volume fading and static/music mixing.

### 2. Build on the Pi

```sh
cd ~/radio
CGO_ENABLED=1 go build -tags pi -o radio .
```

### 3. Copy the librespot binary

Pre-built for Pi 4 (ARM64) and checked in under `bin/`:

```sh
cp bin/librespot-linux-arm64 ~/radio/librespot
chmod +x ~/radio/librespot
```

### 4. Configure

Create `~/radio/.env` on the Pi (use `.env.example` as a template). See the
[Environment Variables](#environment-variables) section for all options.

Minimum required:

```sh
SPOTIFY_CLIENT_ID=<your client id>
SPOTIFY_CLIENT_SECRET=<your client secret>
SPOTIFY_REDIRECT_URI=http://<pi-ip>:8080/auth/callback
```

> `SPOTIFY_REDIRECT_URI` must exactly match the URI registered in your
> [Spotify app settings](https://developer.spotify.com/dashboard).

### 5. Run

```sh
cd ~/radio && ./radio
```

On first run, visit `http://<pi-ip>:8080/auth` in a browser to complete the
Spotify OAuth flow. Credentials are cached and refreshed automatically after
that.

### 6. Verify PipeWire audio mixing

With the radio running and audio playing, confirm both streams are visible:

```sh
pactl list sink-inputs short
```

You should see two entries — one for librespot and one for the static audio
player. If librespot is missing, check that `LIBRESPOT_AUDIO_DEVICE=pulse` is
set in `.env` and that `libasound2-plugins` is installed.

---

## DJ Interstitials

Interstitials are short audio clips (DJ drops, station IDs, etc.) that play
between songs in music mode. They duck the Spotify stream, play the clip, then
restore volume — giving the radio a live-DJ feel.

### How it works

- After each song, the trigger probability increases by
  `INTERSTITIAL_CHANCE_INCREMENT` percent
- When the probability check passes, the clip is scheduled to play ~4 seconds
  before the current track ends
- After an interstitial plays, the counter resets to 0%
- Interstitials only trigger in music mode, never in podcast or speaker mode
- Turning the dial cancels any in-progress clip cleanly

### Clip organization

Place MP3 files under `interstitials/<playlist-slug>/`, where the slug is the
last segment of the Spotify playlist URI:

```
spotify:playlist:37i9dQZF1DXcBWIGoYBM5M  →  interstitials/37i9dQZF1DXcBWIGoYBM5M/
```

```
interstitials/
└── 37i9dQZF1DXcBWIGoYBM5M/
    ├── drop-01.mp3
    ├── drop-02.mp3
    └── station-id.mp3
```

Any number of clips per playlist; one is chosen at random each time.

### Generating clips with ElevenLabs

The `gen-interstitial` tool generates DJ clips via the ElevenLabs text-to-speech
API and saves them directly into the right directory:

```sh
go build -o gen-interstitial ./cmd/gen-interstitial
ELEVENLABS_API_KEY=<key> ./gen-interstitial
```

The tool reads station assignments from the radio database (`DB_PATH`), lets you
pick a station and voice, enter a script, and saves the generated MP3.

---

## Environment Variables

All configuration is via environment variables. Set them in `.env` (loaded
automatically by the install script / systemd service) or export them directly.

### Core

| Variable     | Default    | Description                    |
| ------------ | ---------- | ------------------------------ |
| `PORT`       | `8080`     | HTTP listen port               |
| `DB_PATH`    | `radio.db` | Path to the SQLite database    |
| `SHOW_DEBUG` | `false`    | Show the Debug link in the nav |

### Spotify

| Variable                | Default      | Description                                               |
| ----------------------- | ------------ | --------------------------------------------------------- |
| `SPOTIFY_CLIENT_ID`     | _(required)_ | Spotify app client ID                                     |
| `SPOTIFY_CLIENT_SECRET` | _(required)_ | Spotify app client secret                                 |
| `SPOTIFY_REDIRECT_URI`  | _(required)_ | OAuth redirect URI — must match your Spotify app settings |

### librespot

| Variable                 | Default           | Description                                                                     |
| ------------------------ | ----------------- | ------------------------------------------------------------------------------- |
| `LIBRESPOT_BIN`          | `librespot`       | Path to the librespot binary                                                    |
| `LIBRESPOT_DEVICE_NAME`  | `Zenith Radio`    | Spotify Connect device name                                                     |
| `LIBRESPOT_DEVICE_TYPE`  | `speaker`         | Spotify Connect device type                                                     |
| `LIBRESPOT_CACHE_DIR`    | `librespot-cache` | Directory for librespot credential and file cache                               |
| `LIBRESPOT_AUDIO_DEVICE` | _(empty)_         | ALSA device for librespot output (e.g. `pulse`); empty uses librespot's default |

### Dial & tuning

| Variable                | Default | Description                                                  |
| ----------------------- | ------- | ------------------------------------------------------------ |
| `BUCKET_COUNT`          | `12`    | Number of dial stations (fixed at startup)                   |
| `DIAL_I2C_BUS`          | `1`     | I2C bus number for the TMAG5273 Hall effect sensor           |
| `DIAL_I2C_ADDR`         | `0x35`  | I2C address of the sensor                                    |
| `DIAL_CENTER_X`         | `0`     | X-axis magnetic center offset (from `cmd/dial-calibrate`)    |
| `DIAL_CENTER_Y`         | `0`     | Y-axis magnetic center offset (from `cmd/dial-calibrate`)    |
| `DIAL_MIN_ANGLE`        | `0`     | Start of usable arc in degrees (from `cmd/dial-calibrate`)   |
| `DIAL_MAX_ANGLE`        | `270`   | End of usable arc in degrees (from `cmd/dial-calibrate`)     |
| `DIAL_TUNE_FORGIVENESS` | `0.4`   | Fraction of bucket width that counts as the sweet spot (0–1) |
| `DIAL_STATIC_MIN_GAIN`  | `0.25`  | Minimum static noise gain when outside the sweet spot (0–1)  |

### Audio

| Variable             | Default            | Description                                                     |
| -------------------- | ------------------ | --------------------------------------------------------------- |
| `STATIC_AUDIO_FILES` | `static/noise.mp3` | Comma-separated list of MP3 files for no-signal static playback |
| `AMP_GPIO_PIN`       | `18`               | GPIO pin number for the amplifier mute control                  |

### Volume

| Variable             | Default          | Description                                           |
| -------------------- | ---------------- | ----------------------------------------------------- |
| `VOLUME_SPI_DEV`     | `/dev/spidev0.0` | SPI device for the volume potentiometer               |
| `VOLUME_SPI_CHANNEL` | `0`              | SPI channel                                           |
| `ALSA_CARD`          | `0`              | ALSA card index for volume control                    |
| `ALSA_MIXER_CONTROL` | `Master`         | ALSA mixer control name                               |
| `VOLUME_MIN_RAW`     | `0`              | Raw ADC value at minimum rotation                     |
| `VOLUME_MAX_RAW`     | `1023`           | Raw ADC value at maximum rotation                     |
| `VOLUME_MAX_PCT`     | `100`            | Maximum software volume percent (useful for headroom) |
| `VOLUME_CURVE`       | `linear`         | Volume curve shape (`linear` or `log`)                |

### Toggle

| Variable            | Default      | Description                             |
| ------------------- | ------------ | --------------------------------------- |
| `TOGGLE_GPIO_PIN_A` | _(required)_ | GPIO pin for AM/music toggle position   |
| `TOGGLE_GPIO_PIN_B` | _(required)_ | GPIO pin for FM/podcast toggle position |

### Power

| Variable         | Default      | Description                   |
| ---------------- | ------------ | ----------------------------- |
| `POWER_GPIO_PIN` | _(required)_ | GPIO pin for the power switch |

### Interstitials

| Variable                        | Default         | Description                                                                  |
| ------------------------------- | --------------- | ---------------------------------------------------------------------------- |
| `INTERSTITIAL_DIR`              | `interstitials` | Root directory for DJ clips (`<dir>/<playlist-slug>/*.mp3`)                  |
| `INTERSTITIAL_DUCK_LEVEL`       | `20`            | Spotify volume % while an interstitial plays (0–100)                         |
| `INTERSTITIAL_CHANCE_INCREMENT` | `10`            | Percentage added to trigger probability per song since the last interstitial |

### Images

| Variable          | Default       | Description                                 |
| ----------------- | ------------- | ------------------------------------------- |
| `IMAGE_CACHE_DIR` | `image-cache` | Directory for downloaded playlist cover art |

---

## Development

Run locally against a dev Spotify app:

```sh
cp .env.example .env
# fill in SPOTIFY_CLIENT_ID, SPOTIFY_CLIENT_SECRET
LIBRESPOT_BIN=./bin/librespot-darwin-amd64 go run .
```

Hardware-dependent packages (`hardware/`) only work on the Pi. All other
packages compile and run on macOS/Linux/Windows. On macOS, `oto` uses CoreAudio
via `purego` (no CGO needed); on Linux outside the Pi, `libasound2-dev` and
`CGO_ENABLED=1` are required.

---

## How librespot events work

The `radio` binary doubles as the librespot `--onevent` handler. When librespot
fires a playback event it spawns `radio` with event data in env vars; the binary
detects this via `PLAYER_EVENT` being set, forwards the event over a Unix socket
to the main process, and exits. No separate helper binary is needed.

---

## librespot binaries

Pre-built binaries are checked in under `bin/`:

| File                              | Target                       |
| --------------------------------- | ---------------------------- |
| `bin/librespot-linux-arm64`       | Raspberry Pi 4 (deploy this) |
| `bin/librespot-darwin-amd64`      | macOS Intel (local dev)      |
| `bin/librespot-windows-amd64.exe` | Windows (local dev)          |

These are built at v0.8.0 with the ALSA backend (Pi) or rodio backend
(macOS/Windows).

### Rebuilding for Raspberry Pi 4

Cross-compiling from macOS or Linux using
[`cross`](https://github.com/cross-rs/cross):

**Prerequisites:** Docker Desktop, Rust toolchain (`rustup`), and
`cargo install cross --git https://github.com/cross-rs/cross`

1. Clone librespot:

    ```sh
    git clone https://github.com/librespot-org/librespot
    cd librespot && git checkout v0.8.0
    ```

2. Create `Cross.toml`:

    ```toml
    [target.aarch64-unknown-linux-gnu]
    pre-build = [
        "dpkg --add-architecture arm64",
        "apt-get update && apt-get install --assume-yes libasound2-dev:arm64"
    ]
    ```

3. Build:

    ```sh
    cross build --release --target aarch64-unknown-linux-gnu \
        --no-default-features --features alsa-backend,rustls-tls-webpki-roots
    ```

4. Binary: `target/aarch64-unknown-linux-gnu/release/librespot`

**Alternative: build natively on the Pi** (takes ~10–15 min on a Pi 4)

```sh
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
sudo apt-get install libasound2-dev
git clone https://github.com/librespot-org/librespot
cd librespot && git checkout v0.8.0
cargo build --release --no-default-features --features alsa-backend,rustls-tls-webpki-roots
```

### Rebuilding for macOS / Windows

```sh
git clone https://github.com/librespot-org/librespot
cd librespot && git checkout v0.8.0
cargo build --release --no-default-features --features rodio-backend
```
