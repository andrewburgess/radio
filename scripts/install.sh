#!/usr/bin/env bash
# install.sh — build and install the Zenith Radio service on a Raspberry Pi.
#
# Run from the repo root:
#   ./scripts/install.sh
#
# Re-running is safe: the binary and static assets are always overwritten, but
# /opt/radio/.env is never touched if it already exists.
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

echo "==> Installing static assets..."
sudo cp static/noise.mp3 "$INSTALL_DIR/static/" 2>/dev/null || true
sudo cp static/noise-2.mp3 "$INSTALL_DIR/static/" 2>/dev/null || true

echo "==> Installing librespot..."
sudo cp "$LIBRESPOT_BIN" "$INSTALL_DIR/librespot"
sudo chmod 755 "$INSTALL_DIR/librespot"

# ---------------------------------------------------------------------------
# 4. Config — only copy the example if no .env exists yet
# ---------------------------------------------------------------------------
if [ ! -f "$INSTALL_DIR/.env" ]; then
    echo "==> No .env found — copying .env.example to $INSTALL_DIR/.env"
    sudo cp .env.example "$INSTALL_DIR/.env"
    sudo chown "$USER:$USER" "$INSTALL_DIR/.env"
    sudo chmod 600 "$INSTALL_DIR/.env"
    echo ""
    echo "    *** Edit $INSTALL_DIR/.env before starting the service. ***"
    echo ""
else
    echo "==> $INSTALL_DIR/.env already exists — leaving it untouched"
fi

# ---------------------------------------------------------------------------
# 5. systemd service
# ---------------------------------------------------------------------------
echo "==> Installing systemd service..."
sed "s/__USER__/$USER/" scripts/radio.service \
    | sudo tee /etc/systemd/system/radio.service > /dev/null

sudo systemctl daemon-reload
sudo systemctl enable radio
echo "    Service enabled (radio.service)"

# ---------------------------------------------------------------------------
# 6. Done
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
