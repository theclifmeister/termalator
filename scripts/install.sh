#!/bin/sh
# Installs (or upgrades) terminatr's tm on Linux and macOS.
#
# Downloads the release tarball for this machine (amd64 or arm64), checks
# its sha256 against the release's checksums.txt and puts tm in
# ~/.local/bin. No root needed.
#
#   curl -fsSL https://github.com/theclifmeister/terminatr/releases/latest/download/install.sh | sh
#
# To pass options, save the script and run it, or use `sh -s --`:
#
#   curl -fsSL .../install.sh | sh -s -- --version v0.5.0
#
# Options:
#   --version TAG   a release tag such as v0.5.0 (default: the latest)
#   --dir DIR       where to install (default: ~/.local/bin)
#   --from DIR      a folder holding the release's tarball and checksums.txt,
#                   instead of downloading them (for testing)
set -eu

repo=https://github.com/theclifmeister/terminatr
version=latest
dir=${HOME:-}/.local/bin
from=

die() { echo "install.sh: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case $1 in
    --version) [ $# -ge 2 ] || die "--version needs a value"; version=$2; shift 2 ;;
    --dir) [ $# -ge 2 ] || die "--dir needs a value"; dir=$2; shift 2 ;;
    --from) [ $# -ge 2 ] || die "--from needs a value"; from=$2; shift 2 ;;
    -h|--help) sed -n '2,/^set -eu/p' "$0" | sed '$d;s/^# \{0,1\}//'; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done
[ -n "$dir" ] && [ "$dir" != /.local/bin ] || die "HOME is not set; pass --dir"

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) die "terminatr has no build for $(uname -s) (Linux and macOS only; on Windows use install.ps1)" ;;
esac
case $(uname -m) in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) die "terminatr has no build for $(uname -m) (amd64 and arm64 only)" ;;
esac
archive=tm_${os}_${arch}.tar.gz

if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d ' ' -f 1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
else
  die "needs sha256sum or shasum to check the download"
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/tm-install.XXXXXX") || die "can't make a temporary folder"
trap 'rm -rf "$tmp"' EXIT INT TERM

if [ -n "$from" ]; then
  cp "$from/$archive" "$from/checksums.txt" "$tmp/" || die "no $archive and checksums.txt in $from"
else
  if [ "$version" = latest ]; then
    base=$repo/releases/latest/download
  else
    base=$repo/releases/download/$version
  fi
  command -v curl >/dev/null 2>&1 || die "needs curl"
  for f in "$archive" checksums.txt; do
    echo "downloading $base/$f"
    curl -fsSL -o "$tmp/$f" "$base/$f" || die "can't download $base/$f"
  done
fi

# checksums.txt lines are "<sha256>  <name>".
want=$(awk -v n="$archive" '{ f = $2; sub(/^\*/, "", f); if (f == n) { print tolower($1); exit } }' "$tmp/checksums.txt")
[ -n "$want" ] || die "checksums.txt has no sha256 for $archive"
got=$(sha256 "$tmp/$archive")
[ "$got" = "$want" ] || die "$archive: sha256 $got does not match checksums.txt ($want)"
echo "checksum   ok ($archive)"

mkdir "$tmp/x"
tar -xzf "$tmp/$archive" -C "$tmp/x" tm || die "$archive holds no tm"
mkdir -p "$dir"
# Copy beside the target, then rename over it: replacing a running tm is safe.
cp "$tmp/x/tm" "$dir/.tm.new.$$" && chmod 755 "$dir/.tm.new.$$" && mv -f "$dir/.tm.new.$$" "$dir/tm" ||
  { rm -f "$dir/.tm.new.$$"; die "can't write $dir/tm"; }

echo "installed  $("$dir/tm" version 2>&1 | head -n 1) at $dir/tm"

case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "$dir is not on your PATH. Add it:  export PATH=\"$dir:\$PATH\"  (put that line in your shell's profile)" ;;
esac
echo "Next: tm doctor"
