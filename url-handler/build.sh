#!/bin/bash
set -euo pipefail

APP_NAME="ttyrannosaurus-url-handler"
BUILD_DIR="$(cd "$(dirname "$0")" && pwd)/build"
APP_DIR="$BUILD_DIR/$APP_NAME.app"
MACOS_DIR="$APP_DIR/Contents/MacOS"
LSREGISTER=/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister

echo "Building $APP_NAME..."
mkdir -p "$MACOS_DIR"

# Compile — no Xcode project needed.
swiftc \
  -target arm64-apple-macosx13.0 \
  -O \
  -o "$MACOS_DIR/$APP_NAME" \
  "$(dirname "$0")/URLHandlerApp.swift"

cp "$(dirname "$0")/Info.plist" "$APP_DIR/Contents/Info.plist"

# Ad-hoc codesign (required on macOS 13+ for local execution without notarization).
codesign --force --deep --sign - "$APP_DIR"

echo "Registering ttyrannosaurus:// URL scheme..."
"$LSREGISTER" -f "$APP_DIR"

echo "Built: $APP_DIR"
echo "Test with: open 'ttyrannosaurus://claude/new'"
