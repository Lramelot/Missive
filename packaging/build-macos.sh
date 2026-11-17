#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$ROOT/target/packaging/macos"
OUT="$WORK/build"
RELEASES="$WORK/releases"

VERSION="$(awk '/^\[workspace\.package\]/{f=1;next} /^\[/{f=0} f && /^version[[:space:]]*=/{gsub(/.*=[[:space:]]*"|".*/,"");print;exit}' "$ROOT/Cargo.toml")"
if [ -z "$VERSION" ]; then
  echo "Version introuvable dans Cargo.toml" >&2
  exit 1
fi
echo "Version : $VERSION"

rm -rf "$OUT"
mkdir -p "$OUT/arm64" "$OUT/x86_64" "$OUT/universal" "$RELEASES"

rustup target add aarch64-apple-darwin x86_64-apple-darwin

for pair in "arm64:aarch64-apple-darwin:arm64" "x86_64:x86_64-apple-darwin:amd64"; do
  IFS=: read -r dir triple goarch <<<"$pair"
  (cd "$ROOT/bridge" && CGO_ENABLED=0 GOOS=darwin GOARCH="$goarch" go build -ldflags "-X main.bridgeVersion=$VERSION" -o "$OUT/$dir/missive-bridge" .)
  (cd "$ROOT" && cargo build --release --package missive --target "$triple")
  cp "$ROOT/target/$triple/release/missive" "$OUT/$dir/missive"
done

lipo -create -output "$OUT/universal/missive" "$OUT/arm64/missive" "$OUT/x86_64/missive"
lipo -create -output "$OUT/universal/missive-bridge" "$OUT/arm64/missive-bridge" "$OUT/x86_64/missive-bridge"

APP="$OUT/Missive.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$OUT/universal/missive" "$APP/Contents/MacOS/missive"
cp "$OUT/universal/missive-bridge" "$APP/Contents/MacOS/missive-bridge"
chmod +x "$APP/Contents/MacOS/missive" "$APP/Contents/MacOS/missive-bridge"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key>
  <string>Missive</string>
  <key>CFBundleDisplayName</key>
  <string>Missive</string>
  <key>CFBundleIdentifier</key>
  <string>app.missive.desktop</string>
  <key>CFBundleExecutable</key>
  <string>missive</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleVersion</key>
  <string>$VERSION</string>
  <key>CFBundleShortVersionString</key>
  <string>$VERSION</string>
  <key>LSMinimumSystemVersion</key>
  <string>11.0</string>
  <key>LSUIElement</key>
  <false/>
  <key>NSHighResolutionCapable</key>
  <true/>
</dict>
</plist>
PLIST

if [ -f "$ROOT/packaging/missive.icns" ]; then
  cp "$ROOT/packaging/missive.icns" "$APP/Contents/Resources/missive.icns"
fi

codesign --force --deep -s - "$APP"

if ! command -v vpk >/dev/null 2>&1; then
  echo
  echo "vpk (Velopack) est introuvable : le paquet n'a pas été généré."
  echo "Installation : dotnet tool install -g vpk --version 1.2.161"
  echo "Bundle prêt : $APP"
  exit 0
fi

if [ -n "${MISSIVE_UPDATE_REPO:-}" ]; then
  DOWNLOAD=(download github --repoUrl "https://github.com/$MISSIVE_UPDATE_REPO" --channel osx --outputDir "$RELEASES")
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    DOWNLOAD+=(--token "$GITHUB_TOKEN")
  fi
  vpk "${DOWNLOAD[@]}" || echo "Aucune release précédente téléchargée, pas de delta."
fi

PACK=(pack
  --packId Missive
  --packVersion "$VERSION"
  --packDir "$APP"
  --mainExe missive
  --packTitle Missive
  --packAuthors "Missive contributors"
  --channel osx
  --outputDir "$RELEASES")
if [ -n "${MISSIVE_SIGN_IDENTITY:-}" ]; then
  PACK+=(--signAppIdentity "$MISSIVE_SIGN_IDENTITY")
fi
if [ -n "${MISSIVE_NOTARY_PROFILE:-}" ]; then
  PACK+=(--notaryProfile "$MISSIVE_NOTARY_PROFILE")
fi

vpk "${PACK[@]}"

echo "Livrables : $RELEASES"
