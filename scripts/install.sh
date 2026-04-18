#!/usr/bin/env bash
# install.sh - build and install the Zenith Radio service on a Raspberry Pi.
#
# Run from the repo root:
#   ./scripts/install.sh
#
# Re-running is safe: the binary and static assets are always overwritten.
# .env: copied from local .env if present, otherwise .env.example (only when
#       no .env exists in the install dir yet).
# interstitials/: synced from local interstitials/ if the directory exists.
# static/:        all *.mp3 files from local static/ are copied.
set -euo pipefail

INSTALL_DIR=/opt/radio
SERVICE_NAME=radio
LIBRESPOT_BIN=bin/librespot-linux-arm64

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
# 6. systemd service + cache cleanup timer (sudo required for /etc/systemd)
# ---------------------------------------------------------------------------
echo "==> Installing systemd service..."
sed "s/__USER__/$USER/" scripts/radio.service \
    | sudo tee /etc/systemd/system/radio.service > /dev/null

sudo cp scripts/radio-cache-cleanup.service /etc/systemd/system/radio-cache-cleanup.service
sudo cp scripts/radio-cache-cleanup.timer   /etc/systemd/system/radio-cache-cleanup.timer

sudo systemctl daemon-reload
sudo systemctl enable radio
sudo systemctl enable --now radio-cache-cleanup.timer
echo "    Services enabled (radio.service, radio-cache-cleanup.timer)"

# ---------------------------------------------------------------------------
# 7. Done
# ---------------------------------------------------------------------------
echo ""
echo "Install complete."
echo ""
echo "Useful commands:"
echo "  sudo systemctl start radio      # start now"
echo "  sudo systemctl restart radio    # restart after a reinstall"
echo "  sudo systemctl status radio     # check status"
echo "  journalctl -u radio -f          # follow logs"
echo "  journalctl -u radio -f --output cat  # plain log output (easier to read)"
