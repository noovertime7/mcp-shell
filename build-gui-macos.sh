#!/usr/bin/env bash
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"
if [[ "$(uname -s)" != Darwin ]]; then
  echo 'Build the macOS app on a Mac with Xcode Command Line Tools installed.' >&2
  exit 1
fi
case "$(uname -m)" in
  arm64) rust_target=aarch64-apple-darwin; go_arch=arm64 ;;
  x86_64) rust_target=x86_64-apple-darwin; go_arch=amd64 ;;
  *) echo 'Unsupported Mac architecture' >&2; exit 1 ;;
esac
for tool in go cargo xcrun ditto codesign; do
  command -v "$tool" >/dev/null || { echo "Missing $tool. See docs/desktop.md." >&2; exit 1; }
done
# Use a private staging directory; never recursively remove an existing app.
stage=$(mktemp -d "${TMPDIR:-/tmp}/mcp-shell-build.XXXXXX")
trap 'rm -rf "$stage"' EXIT
app="$stage/MCP Shell.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources" dist
export MACOSX_DEPLOYMENT_TARGET=12.0
GOOS=darwin GOARCH="$go_arch" CGO_ENABLED=0 go build -trimpath -o "$app/Contents/MacOS/mcp-shell" ./cmd/mcp-shell
cargo build --locked --release --manifest-path gui/Cargo.toml --target "$rust_target"
cp "gui/target/$rust_target/release/mcp-shell-gui" "$app/Contents/MacOS/"
if command -v cloudflared >/dev/null; then
  cp "$(command -v cloudflared)" "$app/Contents/MacOS/cloudflared"
elif [[ -x bin/cloudflared ]]; then
  cp bin/cloudflared "$app/Contents/MacOS/cloudflared"
else
  echo 'cloudflared is not installed. Run brew install cloudflared to bundle it.' >&2
  exit 1
fi
cat > "$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleName</key><string>MCP Shell</string>
<key>CFBundleDisplayName</key><string>MCP Shell</string>
<key>CFBundleIdentifier</key><string>io.mcpshell.desktop</string>
<key>CFBundleExecutable</key><string>mcp-shell-gui</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>0.1.0</string>
<key>CFBundleVersion</key><string>1</string>
<key>LSMinimumSystemVersion</key><string>12.0</string>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
PLIST
cp LICENSE "$app/Contents/Resources/LICENSE"
cp docs/desktop.md "$app/Contents/Resources/使用说明.md"
for executable in "$app/Contents/MacOS/"*; do
  chmod +x "$executable"
  codesign --force --sign - "$executable"
done
codesign --force --sign - "$app"
codesign --verify --deep --strict "$app"
archive="$PWD/dist/mcp-shell-desktop-macos-$go_arch.zip"
ditto -c -k --sequesterRsrc --keepParent "$app" "$archive"
echo "macOS app package: $archive"
echo 'Extract the ZIP, then open MCP Shell.app. This is a local ad-hoc build, not a notarized release.'
