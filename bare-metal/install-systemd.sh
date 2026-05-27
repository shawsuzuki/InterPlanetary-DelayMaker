#!/bin/bash
# Install the systemd unit so the delaybox stack starts on boot.
# Idempotent: re-running overwrites the unit with the current repo path.
#
# Usage:
#   sudo ./bare-metal/install-systemd.sh           # install + enable
#   sudo ./bare-metal/install-systemd.sh --uninstall
set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
UNIT_NAME="delaybox.service"
UNIT_SRC="$REPO_ROOT/bare-metal/$UNIT_NAME"
UNIT_DST="/etc/systemd/system/$UNIT_NAME"
DOCKER_BIN="$(command -v docker || echo /usr/bin/docker)"

if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: must be run as root (use sudo)" >&2
    exit 1
fi

if [ "${1:-}" = "--uninstall" ]; then
    systemctl disable --now "$UNIT_NAME" 2>/dev/null || true
    rm -f "$UNIT_DST"
    systemctl daemon-reload
    echo "Uninstalled $UNIT_NAME"
    exit 0
fi

if [ ! -f "$REPO_ROOT/.env" ]; then
    echo "ERROR: $REPO_ROOT/.env not found. Run bare-metal/setup.sh first." >&2
    exit 1
fi

# Substitute placeholders into the unit template
sed \
    -e "s|__REPO_ROOT__|$REPO_ROOT|g" \
    -e "s|/usr/bin/docker|$DOCKER_BIN|g" \
    "$UNIT_SRC" > "$UNIT_DST"

systemctl daemon-reload
systemctl enable "$UNIT_NAME"

echo "Installed: $UNIT_DST"
echo "Enabled:   $UNIT_NAME will start on boot"
echo
echo "Commands:"
echo "  sudo systemctl start $UNIT_NAME       # start now"
echo "  sudo systemctl stop $UNIT_NAME        # stop"
echo "  sudo systemctl status $UNIT_NAME      # status"
echo "  journalctl -u $UNIT_NAME -f           # follow logs"
echo "  sudo ./bare-metal/install-systemd.sh --uninstall"
