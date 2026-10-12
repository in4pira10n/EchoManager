#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SOURCE="$ROOT/assets/icons/EchoManager.png"
ASSETS="$ROOT/web/static/assets"

command -v python3 >/dev/null
command -v go >/dev/null

python3 - "$SOURCE" "$ASSETS" <<'PY'
from pathlib import Path
import sys

from PIL import Image

source = Image.open(sys.argv[1]).convert("RGBA")
assets = Path(sys.argv[2])
sizes = {
    "echomanager-icon.png": 512,
    "pwa-icon-192.png": 192,
    "apple-touch-icon.png": 180,
    "favicon-32.png": 32,
    "favicon-16.png": 16,
}
for filename, size in sizes.items():
    rendered = source.resize((size, size), Image.Resampling.LANCZOS)
    rendered.save(assets / filename, format="PNG", optimize=True)

source.resize((512, 512), Image.Resampling.LANCZOS).save(
    assets / "favicon.ico",
    format="ICO",
    sizes=[(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)],
)
PY

if [ "$(uname -s)" = "Darwin" ] && command -v iconutil >/dev/null 2>&1; then
  ICONSET_ROOT=$(mktemp -d)
  ICONSET="$ICONSET_ROOT/AppIcon.iconset"
  mkdir -p "$ICONSET"
  python3 - "$SOURCE" "$ICONSET" <<'PY'
from pathlib import Path
import sys

from PIL import Image

source = Image.open(sys.argv[1]).convert("RGBA")
iconset = Path(sys.argv[2])
variants = {
    "icon_16x16.png": 16,
    "icon_16x16@2x.png": 32,
    "icon_32x32.png": 32,
    "icon_32x32@2x.png": 64,
    "icon_128x128.png": 128,
    "icon_128x128@2x.png": 256,
    "icon_256x256.png": 256,
    "icon_256x256@2x.png": 512,
    "icon_512x512.png": 512,
    "icon_512x512@2x.png": 1024,
}
for filename, size in variants.items():
    source.resize((size, size), Image.Resampling.LANCZOS).save(iconset / filename)
PY
  iconutil --convert icns --output "$ROOT/assets/icons/AppIcon.icns" "$ICONSET"
fi

go run github.com/akavel/rsrc@v0.10.2 \
  -ico "$ASSETS/favicon.ico" -arch amd64 -o "$ROOT/rsrc_windows_amd64.syso"
