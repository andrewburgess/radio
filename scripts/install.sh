#!/usr/bin/env bash
# install.sh - build and install the Zenith Radio service on a Raspberry Pi.
#
# Run from the repo root:
#   ./scripts/install.sh
#
# Re-running is safe: the binary and static assets are always overwritten.
# .env: only written on first install (never overwritten on reinstall).
# interstitials/: synced from local interstitials/ if the directory exists.
# static/:        all *.mp3 files from local static/ are copied.
set -euo pipefail

INSTALL_DIR=/opt/radio
LIBRESPOT_BIN=bin/librespot-linux-arm64
USER_SYSTEMD_DIR="$HOME/.config/systemd/user"

# ---------------------------------------------------------------------------
# 1. Build
# ---------------------------------------------------------------------------
echo "==> Building radio binary..."
CGO_ENABLED=1 go build -tags pi -o radio .
echo "    OK"

# ---------------------------------------------------------------------------
# 2. Install directories (sudo only here; chown so the rest runs as $USER)
# ---------------------------------------------------------------------------
echo "==> Creating install directory $INSTALL_DIR..."
sudo mkdir -p "$INSTALL_DIR/static"
sudo mkdir -p "$INSTALL_DIR/interstitials"
sudo chown -R "$USER:$USER" "$INSTALL_DIR"

# ---------------------------------------------------------------------------
# 3. Copy binary and assets
# ---------------------------------------------------------------------------
echo "==> Installing binary..."
cp radio "$INSTALL_DIR/radio"
chmod 755 "$INSTALL_DIR/radio"
sudo setcap 'cap_net_bind_service=+ep' "$INSTALL_DIR/radio"

echo "==> Installing librespot..."
cp "$LIBRESPOT_BIN" "$INSTALL_DIR/librespot"
chmod 755 "$INSTALL_DIR/librespot"

echo "==> Installing static audio files..."
shopt -s nullglob
static_files=(static/*.mp3)
shopt -u nullglob
if [ ${#static_files[@]} -gt 0 ]; then
    cp "${static_files[@]}" "$INSTALL_DIR/static/"
    echo "    Copied ${#static_files[@]} file(s) from static/"
else
    echo "    No .mp3 files found in static/ - skipping"
fi

# ---------------------------------------------------------------------------
# 4. Config - only write .env if one doesn't already exist in the install dir
# ---------------------------------------------------------------------------
if [ -f "$INSTALL_DIR/.env" ]; then
    echo "==> $INSTALL_DIR/.env already exists - leaving it untouched"
elif [ -f ".env" ]; then
    echo "==> Copying local .env to $INSTALL_DIR/.env"
    cp .env "$INSTALL_DIR/.env"
    chmod 600 "$INSTALL_DIR/.env"
else
    echo "==> No .env found - copying .env.example to $INSTALL_DIR/.env"
    cp .env.example "$INSTALL_DIR/.env"
    chmod 600 "$INSTALL_DIR/.env"
    echo ""
    echo "    *** Edit $INSTALL_DIR/.env before starting the service. ***"
    echo ""
fi

# ---------------------------------------------------------------------------
# 5. Interstitials - sync from local directory if it exists
# ---------------------------------------------------------------------------
if [ -d "interstitials" ]; then
    echo "==> Syncing interstitials/ to $INSTALL_DIR/interstitials/..."
    rsync -a --info=stats2 interstitials/ "$INSTALL_DIR/interstitials/"
else
    echo "==> No local interstitials/ directory - skipping"
fi

# ---------------------------------------------------------------------------
# 6. User systemd service + cache cleanup timer
#    User services start after the user's PipeWire session is ready, which
#    avoids the race condition where a system service starts before PipeWire.
#    loginctl enable-linger ensures the user session starts at boot.
# ---------------------------------------------------------------------------
echo "==> Installing user systemd service..."
mkdir -p "$USER_SYSTEMD_DIR"
cp scripts/radio.service               "$USER_SYSTEMD_DIR/radio.service"
cp scripts/radio-cache-cleanup.service "$USER_SYSTEMD_DIR/radio-cache-cleanup.service"
cp scripts/radio-cache-cleanup.timer   "$USER_SYSTEMD_DIR/radio-cache-cleanup.timer"

systemctl --user daemon-reload
systemctl --user enable radio
systemctl --user enable --now radio-cache-cleanup.timer
echo "    Services enabled (radio.service, radio-cache-cleanup.timer)"

loginctl enable-linger "$USER"
echo "    Lingering enabled for $USER (user session starts at boot)"

# ---------------------------------------------------------------------------
# 7. Done
# ---------------------------------------------------------------------------
echo ""
echo "Install complete."
echo ""
echo "Useful commands:"
echo "  systemctl --user start radio      # start now"
echo "  systemctl --user restart radio    # restart after a reinstall"
echo "  systemctl --user status radio     # check status"
echo "  journalctl --user -u radio -f     # follow logs"
echo "  journalctl --user -u radio -f --output cat  # plain log output"
