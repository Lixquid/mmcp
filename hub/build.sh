#!/usr/bin/env bash
# Builds the MMCP hub distribution artifacts:
#   dist/MMCP-Hub-windows-amd64.exe   Windows x86-64 executable
#   dist/MMCP-Hub-x86_64.AppImage     Linux AppImage
#
# Requirements:
#   - Linux build host with gcc, X11/GL dev packages (native build)
#   - x86_64-w64-mingw32-gcc for the Windows cross build
#   - appimagetool (downloaded automatically if absent)

set -euo pipefail

cd "$(dirname "$0")"

APP="MMCP-Hub"
DIST="dist"
APPDIR="$APP.AppDir"

# Compute the embedded application version: the hub/ tag naming HEAD when
# present (prefix stripped), otherwise the short commit ID; "-dirty" is
# appended when there are uncommitted changes under the hub folder.
HUB_VER="$(git rev-parse --short HEAD 2>/dev/null || echo dev)"
if git_tag="$(git tag --points-at HEAD 2>/dev/null | grep -E '^hub/v' | head -1)" && [ -n "$git_tag" ]; then
	HUB_VER="${git_tag#hub/}"
fi
if [ -n "$(git status --porcelain -- . 2>/dev/null)" ]; then
	HUB_VER="${HUB_VER}-dirty"
fi
echo "==> Embedding version: $HUB_VER"

rm -rf "$DIST" "$APPDIR"
mkdir -p "$DIST" "$APPDIR/usr/bin"

echo "==> Building Linux binary (for AppImage)"
go build -trimpath -ldflags "-s -w -X main.version=$HUB_VER" -o "$APPDIR/usr/bin/hub" .

echo "==> Building Windows exe"
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc \
	go build -trimpath -ldflags "-H=windowsgui -s -w -X main.version=$HUB_VER" \
	-o "$DIST/$APP-windows-amd64.exe" .

echo "==> Assembling AppDir"
install -Dm644 icon.png "$APPDIR/usr/share/icons/hicolor/256x256/apps/hub.png"
install -Dm644 icon.png "$APPDIR/hub.png"
cp icon.png "$APPDIR/.DirIcon"

cat > "$APPDIR/hub.desktop" <<DESKTOP
[Desktop Entry]
Type=Application
Name=MMCP Hub
Comment=Multicast Media Control Protocol relay and controller
Exec=hub
Icon=hub
Categories=AudioVideo;Audio;Network;
Terminal=false
DESKTOP

cat > "$APPDIR/AppRun" <<'APPRUN'
#!/bin/sh
HERE="$(dirname "$(readlink -f "$0")")"
exec "$HERE/usr/bin/hub" "$@"
APPRUN
chmod +x "$APPDIR/AppRun"

echo "==> Building AppImage"
APPIMAGETOOL="$(dirname "$0")/build/appimagetool"
if [ ! -x "$APPIMAGETOOL" ] && [ ! -x "$APPIMAGETOOL.AppImage" ]; then
	echo "Downloading appimagetool..."
	mkdir -p "$(dirname "$APPIMAGETOOL")"
	curl -fL -o "$APPIMAGETOOL.AppImage" \
		"https://github.com/AppImage/AppImageKit/releases/download/continuous/appimagetool-x86_64.AppImage"
	chmod +x "$APPIMAGETOOL.AppImage"
fi

# Run appimagetool; if FUSE is unavailable, extract and run directly.
if ! "$APPIMAGETOOL.AppImage" --appimage-extract-and-run \
		--comp gzip "$APPDIR" "$DIST/$APP-x86_64.AppImage" 2>/dev/null; then
	if [ ! -x "$APPIMAGETOOL" ]; then
		(cd "$(dirname "$APPIMAGETOOL")" \
			&& "./$(basename "$APPIMAGETOOL").AppImage" --appimage-extract >/dev/null)
		ln -sf squashfs-root/AppRun "$APPIMAGETOOL"
	fi
	"$APPIMAGETOOL" --comp gzip "$APPDIR" "$DIST/$APP-x86_64.AppImage"
fi

rm -rf "$APPDIR"

echo "==> Artifacts:"
ls -la "$DIST"
