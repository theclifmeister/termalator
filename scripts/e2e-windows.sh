#!/bin/sh
# Cross-builds the e2e harness for a Windows test box, from macOS or
# Linux: the scenario binary e2e.test.exe and, in bin/, what E2E_BIN
# names (tm.exe, the apps, fakeagent.exe, and conpty.dll + OpenConsole.exe
# beside tm.exe, as the release ships them, and beside e2e.test.exe).
# CONTRIBUTING.md says how to run them there.
#
#   scripts/e2e-windows.sh [arm64|amd64] OUT
#
# libghostty-vt for the target comes from scripts/release/ghostty.sh
# (.build/release/), or from $GHOSTTY_OUT when set; $TM_ZIG names the Zig
# for cgo (default: the one ghostty.sh links), $CONPTY_DIR the ConPTY
# files (default: conpty.sh's). E2E_RACE is not supported
# (the race detector needs a native toolchain).
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
. "$root/scripts/release/targets.sh"
arch=${1:?usage: scripts/e2e-windows.sh arm64|amd64 OUT}
out=${2:?usage: scripts/e2e-windows.sh arm64|amd64 OUT}
triple=$(zig_triple windows "$arch")
rev=$(sed -n 's/^GHOSTTY_REV *?= *//p' "$root/Makefile" | cut -c1-12)
if [ -z "${GHOSTTY_OUT:-}" ]; then
	"$root/scripts/release/ghostty.sh" "$triple"
	"$root/scripts/release/conpty.sh"
	GHOSTTY_OUT="$root/.build/release/ghostty-$rev-baseline-$triple"
fi
export TM_ZIG=${TM_ZIG:-$root/.build/release/zig}
export TM_ZIG_TARGET="$triple" CC="$root/scripts/release/cc.sh"
export GOOS=windows GOARCH="$arch" CGO_ENABLED=1
export PKG_CONFIG_PATH="$GHOSTTY_OUT/share/pkgconfig"
export CGO_CFLAGS="-O2 -g -DTM_LIBGHOSTTY=$GHOSTTY_OUT"
mkdir -p "$out/bin"
cd "$root"
go build -o "$out/bin/tm.exe" ./cmd/tm
for app in internal/e2e/apps/*/; do
	go build -o "$out/bin/$(basename "$app").exe" "./$app"
done
go build -o "$out/bin/fakeagent.exe" ./internal/e2e/fakeagent
go test -c -o "$out/e2e.test.exe" ./internal/e2e
# Golden screens: the scenario binary reads testdata/ from its working
# directory.
rm -rf "$out/testdata"
cp -R internal/e2e/testdata "$out/testdata"
conpty="${CONPTY_DIR:-$root/.build/release/conpty}/$arch"
if [ -d "$conpty" ]; then
	cp "$conpty/conpty.dll" "$conpty/OpenConsole.exe" "$out/bin/"
	# And beside e2e.test.exe, for the harness's own windows: the inbox
	# conhost turns a pasted \n into " \n" on its way to tm attach.
	cp "$conpty/conpty.dll" "$conpty/OpenConsole.exe" "$out/"
else
	echo "e2e-windows: no $conpty (scripts/release/conpty.sh): tm uses the system's ConPTY" >&2
fi
