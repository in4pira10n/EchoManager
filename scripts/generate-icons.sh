#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SOURCE="$ROOT/assets/icons/AppIcon.icns"
ASSETS="$ROOT/web/static/assets"

if [ "$(uname -s)" != "Darwin" ]; then
  printf '%s\n' "Icon generation requires macOS sips."
  exit 1
fi

command -v sips >/dev/null
command -v python3 >/dev/null
command -v go >/dev/null

sips -s format png -z 512 512 "$SOURCE" --out "$ASSETS/echomanager-icon.png" >/dev/null
sips -s format png -z 180 180 "$SOURCE" --out "$ASSETS/apple-touch-icon.png" >/dev/null
sips -s format png -z 32 32 "$SOURCE" --out "$ASSETS/favicon-32.png" >/dev/null
sips -s format png -z 16 16 "$SOURCE" --out "$ASSETS/favicon-16.png" >/dev/null

python3 -c 'from PIL import Image; import sys; Image.open(sys.argv[1]).save(sys.argv[2], format="ICO", sizes=[(16,16),(24,24),(32,32),(48,48),(64,64),(128,128),(256,256)])' \
  "$ASSETS/echomanager-icon.png" "$ASSETS/favicon.ico"

go run github.com/akavel/rsrc@v0.10.2 \
  -ico "$ASSETS/favicon.ico" -arch amd64 -o "$ROOT/rsrc_windows_amd64.syso"
