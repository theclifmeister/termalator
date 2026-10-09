#!/bin/sh
# Stages conpty.dll and OpenConsole.exe (Microsoft's own ConPTY, MIT, from
# microsoft/terminal) for the Windows archives, into
# .build/release/conpty/<amd64|arm64>/. With them next to tm.exe every
# Windows version gets the same console behaviour (OSC and DECSET
# passthrough), instead of whatever conhost the OS ships.
#
#   scripts/release/conpty.sh
#
# The NuGet package is pinned and checked against its sha256.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd)
version=1.25.260930003
sha256=02b07b349af66d801159bdf9e440d4a1ce78bb951f37fc8609731665afdae7ee
out="$root/.build/release/conpty"
[ ! -f "$out/.version-$version" ] || exit 0
mkdir -p "$out"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
pkg="$tmp/conpty.nupkg"
curl -fsSL --retry 3 -o "$pkg" \
	"https://api.nuget.org/v3-flatcontainer/microsoft.windows.console.conpty/$version/microsoft.windows.console.conpty.$version.nupkg"
got=$(shasum -a 256 "$pkg" | cut -d' ' -f1)
[ "$got" = "$sha256" ] || { echo "conpty $version: sha256 $got does not match the pinned checksum" >&2; exit 1; }
unzip -q "$pkg" -d "$tmp/x"
for pair in amd64:x64 arm64:arm64; do
	go=${pair%:*}
	nu=${pair#*:}
	rm -rf "${out:?}/$go"
	mkdir -p "$out/$go"
	cp "$tmp/x/runtimes/win-$nu/native/conpty.dll" "$tmp/x/build/native/runtimes/$nu/OpenConsole.exe" "$out/$go/"
done
rm -f "$out"/.version-*
touch "$out/.version-$version"
