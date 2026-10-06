#!/usr/bin/env bash
# Build DIMA for every supported platform into ./dist
set -euo pipefail

VERSION="${VERSION:-0.2.0}"
OUT="dist"
# Identifies this build, and gives it a hash of its own — see build.ps1.
STAMP="$(date +%Y%m%d-%H%M%S)"

go test ./...

rm -rf "$OUT" && mkdir -p "$OUT"

targets=(
  "windows amd64 .exe"
  "windows arm64 .exe"
  "darwin  amd64 ''"
  "darwin  arm64 ''"
  "linux   amd64 ''"
  "linux   arm64 ''"
)

for t in "${targets[@]}"; do
  read -r os arch ext <<<"$t"
  [ "$ext" = "''" ] && ext=""
  name="dima-${VERSION}-${os}-${arch}${ext}"
  echo "building $name"
  # -H windowsgui keeps the Windows build from opening a console window;
  # console_windows.go reattaches the output when run from a terminal.
  ldflags="-s -w -X main.buildStamp=${STAMP}"
  [ "$os" = "windows" ] && ldflags="$ldflags -H windowsgui"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "$ldflags" -o "$OUT/$name" .
done

echo
ls -lh "$OUT"
