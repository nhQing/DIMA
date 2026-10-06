#!/usr/bin/env bash
# Build DIMA as a proper macOS .app bundle (universal amd64+arm64), with an
# icon converted from ui/icon.svg and an ad-hoc code signature.
#
# Usage:
#   ./build-mac-app.sh              # builds dist/DIMA.app
#   ./build-mac-app.sh --install    # also copies it into /Applications
#
# Only meant to run on macOS (uses lipo, sips, iconutil, codesign).
set -euo pipefail

if [[ "$(uname)" != "Darwin" ]]; then
  echo "error: this script only runs on macOS" >&2
  exit 1
fi

VERSION="${VERSION:-0.2.0}"
APP_NAME="DIMA"
BUNDLE_ID="dev.dima.app"
OUT="dist"
APP_PATH="$OUT/$APP_NAME.app"
STAMP="$(date +%Y%m%d-%H%M%S)"
INSTALL=0

for arg in "$@"; do
  case "$arg" in
    --install) INSTALL=1 ;;
    *) echo "unknown flag: $arg" >&2; exit 1 ;;
  esac
done

go test ./...

rm -rf "$APP_PATH"
mkdir -p "$APP_PATH/Contents/MacOS" "$APP_PATH/Contents/Resources"

echo "building darwin/amd64 + darwin/arm64"
ldflags="-s -w -X main.buildStamp=${STAMP}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "$ldflags" -o "$tmp/dima-amd64" .
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "$ldflags" -o "$tmp/dima-arm64" .
lipo -create -output "$APP_PATH/Contents/MacOS/dima" "$tmp/dima-amd64" "$tmp/dima-arm64"
chmod +x "$APP_PATH/Contents/MacOS/dima"

echo "writing Info.plist"
cat > "$APP_PATH/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key>
  <string>${APP_NAME}</string>
  <key>CFBundleDisplayName</key>
  <string>${APP_NAME}</string>
  <key>CFBundleIdentifier</key>
  <string>${BUNDLE_ID}</string>
  <key>CFBundleVersion</key>
  <string>${VERSION}</string>
  <key>CFBundleShortVersionString</key>
  <string>${VERSION}</string>
  <key>CFBundleExecutable</key>
  <string>dima</string>
  <key>CFBundleIconFile</key>
  <string>icon.icns</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>LSMinimumSystemVersion</key>
  <string>11.0</string>
  <key>NSHighResolutionCapable</key>
  <true/>
  <key>LSApplicationCategoryType</key>
  <string>public.app-category.developer-tools</string>
</dict>
</plist>
PLIST

echo "building icon"
iconset="$tmp/icon.iconset"
mkdir -p "$iconset"
base_png="$tmp/icon-base.png"

icon_ok=0
if command -v qlmanage >/dev/null 2>&1; then
  # Renders the SVG via the QuickLook generator, then sips resizes it down.
  qlmanage -t -s 1024 -o "$tmp" "ui/icon.svg" >/dev/null 2>&1 || true
  if [[ -f "$tmp/icon.svg.png" ]]; then
    mv "$tmp/icon.svg.png" "$base_png"
    icon_ok=1
  fi
fi

if [[ "$icon_ok" -eq 1 ]]; then
  for size in 16 32 128 256 512; do
    sips -z "$size" "$size" "$base_png" --out "$iconset/icon_${size}x${size}.png" >/dev/null
    double=$((size * 2))
    sips -z "$double" "$double" "$base_png" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
  done
  iconutil -c icns "$iconset" -o "$APP_PATH/Contents/Resources/icon.icns"
else
  echo "warning: could not render ui/icon.svg to a .icns icon (qlmanage unavailable" \
       "or render failed) — app will use the generic macOS app icon" >&2
fi

echo "ad-hoc signing"
codesign --force --deep --sign - "$APP_PATH"

echo
echo "built: $APP_PATH"

if [[ "$INSTALL" -eq 1 ]]; then
  echo "installing to /Applications"
  rm -rf "/Applications/$APP_NAME.app"
  ditto "$APP_PATH" "/Applications/$APP_NAME.app"
  echo "installed: /Applications/$APP_NAME.app"
fi
