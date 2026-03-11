#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"

KEY_DIR="/srv/hobnob/license-keys"
SRV_DIR="/srv/hobnob"
BIN_SRC="$REPO_DIR/bin/hobnob-license-manager"
BIN_DST="/usr/local/bin/hobnob-license-manager"
README_SRC="$REPO_DIR/license-manager/README.md"
README_DST="$SRV_DIR/README.md"

# Build if not already built.
if [ ! -f "$BIN_SRC" ]; then
    echo "Building hobnob-license-manager..."
    (cd "$REPO_DIR" && go build -o "$BIN_SRC" ./license-manager)
fi

# Install binary.
echo "Installing $BIN_DST"
sudo cp "$BIN_SRC" "$BIN_DST"
sudo chmod 755 "$BIN_DST"

# Create directories.
echo "Creating $SRV_DIR"
sudo mkdir -p "$KEY_DIR"
sudo chown "$(whoami)" "$SRV_DIR"
sudo chown "$(whoami)" "$KEY_DIR"
chmod 755 "$SRV_DIR"
chmod 700 "$KEY_DIR"

# Copy README.
cp "$README_SRC" "$README_DST"

echo ""
echo "Installed:"
echo "  Binary:    $BIN_DST"
echo "  Key dir:   $KEY_DIR"
echo "  README:    $README_DST"
echo ""
echo "Next steps:"
echo "  hobnob-license-manager keys generate"
echo "  hobnob-license-manager keys inject"
