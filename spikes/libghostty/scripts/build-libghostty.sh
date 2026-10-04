#!/usr/bin/env bash
# Builds libghostty-vt into spikes/libghostty/.deps/ghostty-out using a
# pinned Zig and a pinned Ghostty commit (the same commit go-libghostty
# pins in its CMakeLists.txt). Nothing outside .deps/ is touched.
set -euo pipefail

ZIG_VERSION=0.16.0
GHOSTTY_COMMIT=33da6848d63b3bba2b4f31ab1531d618f2795192
TARGET=${TARGET:-}          # e.g. x86_64-linux-gnu for a cross build

here=$(cd "$(dirname "$0")/.." && pwd)
deps=$here/.deps
mkdir -p "$deps"

os=$(uname -s | tr A-Z a-z); arch=$(uname -m)
[ "$os" = darwin ] && os=macos
[ "$arch" = arm64 ] && arch=aarch64
zigdir=$deps/zig-$arch-$os-$ZIG_VERSION
if [ ! -x "$zigdir/zig" ]; then
  curl -fsSL "https://ziglang.org/download/$ZIG_VERSION/zig-$arch-$os-$ZIG_VERSION.tar.xz" | tar -xJ -C "$deps"
fi
zig=$zigdir/zig

src=$deps/ghostty
if [ ! -d "$src/.git" ]; then
  git clone --filter=blob:none https://github.com/ghostty-org/ghostty.git "$src"
fi
git -C "$src" fetch -q origin "$GHOSTTY_COMMIT" || true
git -C "$src" checkout -q "$GHOSTTY_COMMIT"

out=$deps/ghostty-out${TARGET:+-$TARGET}
cd "$src"
"$zig" build -Demit-lib-vt -Demit-xcframework=false -Doptimize=ReleaseFast \
  ${TARGET:+-Dtarget=$TARGET} --prefix "$out"
echo "built: $out"
ls -la "$out/lib"
