# Zenith Radio

A Go + HTMX application running on a Raspberry Pi 4 inside a retrofitted Zenith
radio cabinet. See [PLAN.md](PLAN.md) for the full architecture and build plan.

---

## Deploying to the Pi

### 1. Cross-compile the radio binary

From your dev machine:

```sh
GOOS=linux GOARCH=arm64 go build -o radio-linux-arm64 .
```

### 2. Copy files to the Pi

```sh
scp radio-linux-arm64 pi@<pi-ip>:~/radio/radio
scp bin/librespot-linux-arm64 pi@<pi-ip>:~/radio/librespot
ssh pi@<pi-ip> chmod +x ~/radio/radio ~/radio/librespot
```

### 3. Configure

Create `~/radio/.env` on the Pi (use `.env.example` as a template):

```sh
PORT=8080
LIBRESPOT_BIN=./librespot
LIBRESPOT_DEVICE_NAME=Zenith Radio
LIBRESPOT_CACHE_DIR=./librespot-cache
SPOTIFY_CLIENT_ID=<your client id>
SPOTIFY_CLIENT_SECRET=<your client secret>
SPOTIFY_REDIRECT_URI=http://<pi-ip>:8080/auth/callback
```

> `SPOTIFY_REDIRECT_URI` must exactly match the URI registered in your
> [Spotify app settings](https://developer.spotify.com/dashboard).

### 4. Run

```sh
cd ~/radio && ./radio
```

On first run, visit `http://<pi-ip>:8080/auth` in a browser to complete the
Spotify OAuth flow. Credentials are cached in `LIBRESPOT_CACHE_DIR` and
refreshed automatically after that.

### 5. How librespot events work

The `radio` binary doubles as the librespot `--onevent` handler. When librespot
fires a playback event it spawns `radio` with event data in env vars; the binary
detects this via `PLAYER_EVENT` being set, forwards the event over a Unix socket
to the main process, and exits. No separate helper binary is needed.

---

## Development

Run locally against a dev Spotify app (set `SPOTIFY_REDIRECT_URI=http://localhost:8080/auth/callback`):

```sh
cp .env.example .env
# fill in SPOTIFY_CLIENT_ID, SPOTIFY_CLIENT_SECRET
LIBRESPOT_BIN=./bin/librespot-darwin-amd64 go run .
```

Hardware-dependent packages (`hardware/`) only work on the Pi. All other
packages compile and run on macOS/Linux/Windows.

---

## librespot

Pre-built binaries are checked in under `bin/`:

| File                          | Target                        |
| ----------------------------- | ----------------------------- |
| `bin/librespot-linux-arm64`   | Raspberry Pi 4 (deploy this)  |
| `bin/librespot-darwin-amd64`  | macOS Intel (local dev)       |
| `bin/librespot-windows-amd64.exe` | Windows (local dev)       |

These are built at v0.8.0 with the ALSA backend (Pi) or rodio backend (macOS/Windows).
If you need to rebuild (e.g. to update the version), see the instructions below.

### Rebuilding for Raspberry Pi 4

Cross-compiling from macOS or Linux using [`cross`](https://github.com/cross-rs/cross):

**Prerequisites**

- Docker Desktop (running)
- Rust toolchain (`rustup`)
- `cross`: `cargo install cross --git https://github.com/cross-rs/cross`

**Steps**

1. Clone librespot:

    ```sh
    git clone https://github.com/librespot-org/librespot
    cd librespot
    git checkout v0.8.0
    ```

2. Create `Cross.toml` to install ALSA headers for ARM64 inside the container:

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

4. Binary output: `target/aarch64-unknown-linux-gnu/release/librespot`

**Alternative: build natively on the Pi** (takes ~10–15 min on a Pi 4)

```sh
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
sudo apt-get install libasound2-dev
git clone https://github.com/librespot-org/librespot
cd librespot && git checkout v0.8.0
cargo build --release --no-default-features --features alsa-backend,rustls-tls-webpki-roots
```

### Rebuilding for macOS (Intel)

```sh
git clone https://github.com/librespot-org/librespot
cd librespot && git checkout v0.8.0
cargo build --release --no-default-features --features rodio-backend
# binary: target/release/librespot
```

### Rebuilding for Windows

```sh
git clone https://github.com/librespot-org/librespot
cd librespot && git checkout v0.8.0
cargo build --release --no-default-features --features rodio-backend
# binary: target/release/librespot.exe
```
