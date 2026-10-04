#!/bin/sh
# Prints the absolute path of the Zig that `make` uses (fetching the pinned
# Zig into .build/ first if needed).
set -eu
root=$(cd "$(dirname "$0")/../.." && pwd)
make -C "$root" --no-print-directory zig-path | tail -n1
