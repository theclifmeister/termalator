#!/bin/sh
# Builds libghostty-vt for every release target (or the targets given as
# arguments) into .build/release/, from a Ghostty checkout of its own so
# that release builds never touch the host build in .build/.
#
#   scripts/release/ghostty.sh [zig-triple ...]
#
# Prints nothing on success but make's output. ZIG overrides the Zig used.
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd)
. "$root/scripts/release/targets.sh"
build="$root/.build/release"
mkdir -p "$build"
zig=${ZIG:-$("$root/scripts/release/zig.sh")}
ln -sfn "$zig" "$build/zig"
[ $# -gt 0 ] || set -- $RELEASE_TARGETS
for t in "$@"; do
	make -C "$root" --no-print-directory ghostty \
		BUILD="$build" ZIG="$zig" GHOSTTY_TARGET="$t"
done
