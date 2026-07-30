#!/bin/sh
# Build the compack Go binary for the current platform, then run a native command.
# Usage: ./build.sh [dev|build|test|check|...]
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ASSETS_DIR="$SCRIPT_DIR/assets"

# Detect OS
case "$(uname -s)" in
  Linux*)  GOOS=linux ;;
  Darwin*) GOOS=darwin ;;
  MINGW*|MSYS*|CYGWIN*) GOOS=windows ;;
  *) echo "Unknown OS: $(uname -s)"; exit 1 ;;
esac

# Detect architecture
case "$(uname -m)" in
  x86_64|amd64) GOARCH=amd64 ;;
  aarch64|arm64) GOARCH=arm64 ;;
  *) echo "Unknown architecture: $(uname -m)"; exit 1 ;;
esac

# Set output binary name
if [ "$GOOS" = "windows" ]; then
  BINARY_NAME="compack.exe"
else
  BINARY_NAME="compack"
fi

mkdir -p "$ASSETS_DIR"

echo "Building compack for $GOOS/$GOARCH..."
GOOS=$GOOS GOARCH=$GOARCH go build -o "$ASSETS_DIR/$BINARY_NAME" "$ROOT_DIR"
echo "Binary built: $ASSETS_DIR/$BINARY_NAME"

# Run the native command with all arguments passed to this script
if [ $# -eq 0 ]; then
  echo "Usage: $0 [dev|build|test|check|...]"
  echo "Example: $0 dev"
  exit 0
fi

exec native "$@"
