#!/bin/bash
# Test AppImage build locally
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

cd "$SCRIPT_DIR"

echo "=== Building compack binary for Linux ==="
mkdir -p assets
GOOS=linux GOARCH=amd64 go build -o assets/compack "$ROOT_DIR"
chmod +x assets/compack

echo "=== Building the app ==="
native build

echo "=== Creating AppDir structure manually ==="
rm -rf compack.AppDir
mkdir -p compack.AppDir/usr/bin
mkdir -p compack.AppDir/usr/share/applications
mkdir -p compack.AppDir/usr/share/icons/hicolor/512x512/apps

# Copy the built binary
cp zig-out/bin/compack compack.AppDir/usr/bin/
chmod +x compack.AppDir/usr/bin/compack

# Copy the compack CLI binary into the AppImage
cp assets/compack compack.AppDir/usr/bin/compack-cli
chmod +x compack.AppDir/usr/bin/compack-cli

# Create desktop file
cat > compack.AppDir/compack.desktop << 'EOF'
[Desktop Entry]
Type=Application
Name=Compack
Comment=A fast Minecraft resource/data pack optimizer
Exec=compack
Icon=app-icon
Categories=Game;Utility;
EOF

# Copy icon to root of AppDir
cp assets/icon.png compack.AppDir/app-icon.png 2>/dev/null || true

# Create AppRun
printf '#!/bin/bash\nHERE="$(dirname "$(readlink -f "$0")")"\nexec "$HERE/usr/bin/compack" "$@"\n' > compack.AppDir/AppRun
chmod +x compack.AppDir/AppRun

echo "=== Downloading appimagetool ==="
if [ ! -f appimagetool-x86_64.appimage ]; then
    wget -q https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-x86_64.appimage
    chmod +x appimagetool-x86_64.appimage
fi

echo "=== Building AppImage ==="
./appimagetool-x86_64.appimage compack.AppDir compack-desktop-x86_64.AppImage

echo "=== AppImage created successfully! ==="
echo "You can test it with: ./compack-desktop-x86_64.AppImage"
echo "Or run directly: ./compack-desktop-x86_64.AppImage --help"
