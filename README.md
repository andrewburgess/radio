# Zenith Radio

A Go + HTMX application running on a Raspberry Pi 4 inside a retrofitted Zenith
radio cabinet. See [PLAN.md](PLAN.md) for the full architecture and build plan.

---

## librespot

Spotify Connect audio playback is handled by
[librespot](https://github.com/librespot-org/librespot) as a managed subprocess.
Binaries for each target platform must be built separately and placed in
`bin/<platform>/librespot[.exe]`.

### Building for Raspberry Pi 4 (from Windows)

The Pi 4 target is `aarch64-unknown-linux-gnu`. Cross-compiling from Windows
requires Docker Desktop and the [`cross`](https://github.com/cross-rs/cross) tool,
which runs the build inside a preconfigured Linux container.

**Prerequisites**

- [Docker Desktop](https://www.docker.com/products/docker-desktop/) — running
- Rust toolchain (`rustup`)
- `cross`: `cargo install cross --git https://github.com/cross-rs/cross`

**Steps**

1. Clone librespot and enter the directory:

    ```sh
    git clone https://github.com/librespot-org/librespot
    cd librespot
    ```

2. Create a `Cross.toml` in the librespot root to install ALSA dev headers for
   the ARM64 target inside the container:

    ```toml
    [target.aarch64-unknown-linux-gnu]
    pre-build = [
        "dpkg --add-architecture arm64",
        "apt-get update && apt-get install --assume-yes libasound2-dev:arm64"
    ]
    ```

3. Build:

    ```sh
    cross build --release --target aarch64-unknown-linux-gnu --no-default-features --features alsa-backend,rustls-tls-webpki-roots
    ```

4. The binary will be at:

    ```
    target/aarch64-unknown-linux-gnu/release/librespot
    ```

**Alternative: build natively on the Pi**

If you have Docker issues or prefer not to cross-compile, SSH into the Pi and
build there. A release build takes roughly 10–15 minutes on a Pi 4.

```sh
# On the Pi
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh
sudo apt-get install libasound2-dev
git clone https://github.com/librespot-org/librespot
cd librespot
cargo build --release --no-default-features --features alsa-backend,rustls-tls-webpki-roots
```

### Building for macOS (Intel)

On the Intel Mac, no cross-compilation is needed:

```sh
git clone https://github.com/librespot-org/librespot
cd librespot
cargo build --release --no-default-features --features rodio-backend
# binary: target/release/librespot
```

### Building for Windows

No cross-compilation needed. From a Developer PowerShell or any shell with the
Rust toolchain:

```sh
cargo build --release --no-default-features --features rodio-backend
# binary: target/release/librespot.exe
```

---

## Radio server

> Build instructions TBD as development progresses. See [PLAN.md](PLAN.md) for
> the phased build sequence.

The server is a Go binary that cross-compiles for `GOARCH=arm64 GOOS=linux`
without CGO (it uses `modernc.org/sqlite`, a pure-Go SQLite driver).
