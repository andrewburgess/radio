#!/usr/bin/env bash
# install.sh — build and install the Zenith Radio service on a Raspberry Pi.
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
# 2. Install directories
# ---------------------------------------------------------------------------
echo "==> Creating install directory $INSTALL_DIR..."
sudo mkdir -p "$INSTALL_DIR/static"
sudo mkdir -p "$INSTALL_DIR/interstitials"
sudo chown "$USER:$USER" "$INSTALL_DIR"

# ---------------------------------------------------------------------------
# 3. Copy binary and assets
# ---------------------------------------------------------------------------
echo "==> Installing binary..."
sudo cp radio "$INSTALL_DIR/radio"
sudo chmod 755 "$INSTALL_DIR/radio"

echo "==> Installing static audio files..."
shopt -s nullglob
static_files=(static/*.mp3)
shopt -u nullglob
if [ ${#static_files[@]} -gt 0 ]; then
    cp "${static_files[@]}" "$INSTALL_DIR/static/"
    echo "    Copied ${#static_files[@]} file(s) from static/"
else
    echo "    No .mp3 files found in static/ — skipping"
fi

echo "==> Installing librespot..."
sudo cp "$LIBRESPOT_BIN" "$INSTALL_DIR/librespot"
sudo chmod 755 "$INSTALL_DIR/librespot"

# ---------------------------------------------------------------------------
# 4. Config — prefer local .env; fall back to .env.example for first install
# ---------------------------------------------------------------------------
if [ -f ".env" ]; then
    echo "==> Copying local .env to $INSTALL_DIR/.env"
    cp .env "$INSTALL_DIR/.env"
    chmod 600 "$INSTALL_DIR/.env"
elif [ ! -f "$INSTALL_DIR/.env" ]; then
    echo "==> No .env found — copying .env.example to $INSTALL_DIR/.env"
    cp .env.example "$INSTALL_DIR/.env"
    chmod 600 "$INSTALL_DIR/.env"
    echo ""
    echo "    *** Edit $INSTALL_DIR/.env before starting the service. ***"
    echo ""
else
    echo "==> No local .env and $INSTALL_DIR/.env already exists — leaving it untouched"
fi

# ---------------------------------------------------------------------------
# 5. Interstitials — sync from local directory if it exists
# ---------------------------------------------------------------------------
if [ -d "interstitials" ]; then
    echo "==> Syncing interstitials/ to $INSTALL_DIR/interstitials/..."
    rsync -a --info=stats2 interstitials/ "$INSTALL_DIR/interstitials/"
else
    echo "==> No local interstitials/ directory — skipping"
fi

# ---------------------------------------------------------------------------
# 6. systemd service
# ---------------------------------------------------------------------------
echo "==> Installing systemd service..."
sed "s/__USER__/$USER/" scripts/radio.service \
    | sudo tee /etc/systemd/system/radio.service > /dev/null

sudo systemctl daemon-reload
sudo systemctl enable radio
echo "    Service enabled (radio.service)"

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
