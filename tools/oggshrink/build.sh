#!/bin/sh
# Builds oggshrink for every compack target with zig cc (statically linked
# against libogg and libvorbis) into optim/bin, next to the other embedded
# tools. Needs zig and curl; the sources are downloaded and checked once.
set -eu
cd "$(dirname "$0")"

OGG=libogg-1.3.5
OGG_SHA256=c4d91be36fc8e54deae7575241e03f4211eb102afb3fc0775fbbc1b740016705
VORBIS=libvorbis-1.3.7
VORBIS_SHA256=b33cc4934322bcbf6efcbacf49e3ca01aadbea4114ec9589d1b1e9d20f72954b
OUT=../../optim/bin

mkdir -p build
fetch() { # name sha256 url
	if [ ! -d "build/$1" ]; then
		curl -sSfL -o "build/$1.tar.xz" "$3"
		echo "$2  build/$1.tar.xz" | sha256sum -c - >/dev/null
		tar -xf "build/$1.tar.xz" -C build
	fi
}
fetch "$OGG" "$OGG_SHA256" "https://downloads.xiph.org/releases/ogg/$OGG.tar.xz"
fetch "$VORBIS" "$VORBIS_SHA256" "https://downloads.xiph.org/releases/vorbis/$VORBIS.tar.xz"

V="build/$VORBIS/lib"
SOURCES="oggshrink.c build/$OGG/src/bitwise.c build/$OGG/src/framing.c
	$V/analysis.c $V/bitrate.c $V/block.c $V/codebook.c $V/envelope.c $V/floor0.c
	$V/floor1.c $V/info.c $V/lookup.c $V/lpc.c $V/lsp.c $V/mapping0.c $V/mdct.c
	$V/psy.c $V/registry.c $V/res0.c $V/sharedbook.c $V/smallft.c $V/synthesis.c
	$V/window.c $V/vorbisenc.c $V/vorbisfile.c"

for t in x86_64-linux-musl:linux_amd64 aarch64-linux-musl:linux_arm64 \
	x86_64-macos:darwin_amd64 aarch64-macos:darwin_arm64 x86_64-windows-gnu:windows_amd64.exe; do
	target=${t%%:*}
	out="$OUT/oggshrink_${t#*:}"
	# -ffp-contract=off: no fused multiply-adds, so every target encodes the
	# same input to the same bytes.
	zig cc -target "$target" -O2 -s -ffp-contract=off -fno-sanitize=all -w \
		-Iinclude -I"build/$OGG/include" -I"build/$VORBIS/include" -I"$V" \
		$SOURCES -lm -o "$out"
	echo "built $out"
done
