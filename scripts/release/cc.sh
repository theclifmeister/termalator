#!/bin/sh
# The C compiler for release builds: zig cc for $TM_ZIG_TARGET, with the
# Zig that scripts/release/ghostty.sh linked into .build/release/zig.
# goreleaser sets CC to this script and TM_ZIG_TARGET per target.
#
# macOS targets link libresolv, which Zig doesn't ship: the macOS SDK's
# .tbd stubs provide it, so darwin builds need a macOS host with the
# Command Line Tools (the release workflow runs on macos-latest).
root=$(cd "$(dirname "$0")/../.." && pwd)
target=${TM_ZIG_TARGET:?TM_ZIG_TARGET is not set}
case $target in
*-macos*)
	sdk=$(xcrun --sdk macosx --show-sdk-path) || exit 1
	set -- "$@" -L"$sdk/usr/lib" -F"$sdk/System/Library/Frameworks"
	;;
esac
zig="$root/.build/release/zig"
# Go probes the compiler with `-### -x c -c -`, which zig cc runs for real,
# leaving a "-.o" in the working directory: probe in a scratch directory.
if [ "${1:-}" = "-###" ]; then
	tmp=$(mktemp -d) || exit 1
	(cd "$tmp" && "$zig" cc -target "$target" "$@")
	rc=$?
	rm -rf "$tmp"
	exit $rc
fi
exec "$zig" cc -target "$target" "$@"
