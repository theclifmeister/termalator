#!/bin/sh
# Checks the release archives in a goreleaser dist/ folder: checksums,
# contents, what each binary links, the glibc floor of the Linux
# binaries, and `tm selftest` for the host's own archive.
#
#   scripts/release/check.sh [dist]
set -eu
dist=${1:-dist}
glibc_floor=2.28
fail() { echo "check: $*" >&2; exit 1; }

(cd "$dist" && shasum -a 256 -c checksums.txt) || fail "checksums do not match"

host_os=$(uname -s | tr A-Z a-z)
host_arch=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
n=0
for a in "$dist"/tm_*.tar.gz; do
	[ -e "$a" ] || fail "no archives in $dist"
	n=$((n + 1))
	name=$(basename "$a" .tar.gz)
	os=$(echo "$name" | awk -F_ '{print $(NF-1)}')
	arch=$(echo "$name" | awk -F_ '{print $NF}')
	d="$tmp/$name"
	mkdir -p "$d"
	tar -xzf "$a" -C "$d"
	for f in tm LICENSE README.md; do
		[ -f "$d/$f" ] || fail "$name lacks $f"
	done
	desc=$(file -b "$d/tm")
	case "$os/$arch" in
	darwin/arm64) want="Mach-O 64-bit executable arm64" ;;
	darwin/amd64) want="Mach-O 64-bit executable x86_64" ;;
	linux/arm64) want="ELF 64-bit LSB executable, ARM aarch64" ;;
	linux/amd64) want="ELF 64-bit LSB executable, x86-64" ;;
	*) fail "$name: unexpected target $os/$arch" ;;
	esac
	case $desc in "$want"*) ;; *) fail "$name: tm is '$desc', want '$want'" ;; esac
	if [ "$os" = darwin ] && command -v otool >/dev/null; then
		libs=$(otool -L "$d/tm" | tail -n +2 | awk '{print $1}' | grep -v -e '^/usr/lib/libSystem\.B\.dylib$' -e '^/usr/lib/libresolv\.9\.dylib$' || true)
		[ -z "$libs" ] || fail "$name links more than libSystem and libresolv: $libs"
	fi
	if [ "$os" = linux ] && command -v objdump >/dev/null; then
		max=$(objdump -T "$d/tm" | grep -o 'GLIBC_[0-9.]*' | sed 's/GLIBC_//' | sort -t. -k1,1n -k2,2n | tail -1)
		top=$(printf '%s\n%s\n' "$max" "$glibc_floor" | sort -t. -k1,1n -k2,2n | tail -1)
		[ "$top" = "$glibc_floor" ] || fail "$name needs glibc $max, above the floor $glibc_floor"
		echo "$name: glibc >= $max"
	fi
	if [ "$os/$arch" = "$host_os/$host_arch" ]; then
		"$d/tm" selftest || fail "$name: tm selftest failed"
		"$d/tm" version
	fi
	echo "$name: ok ($desc)" | cut -c1-120
done
[ "$n" -eq 4 ] || fail "$n archives, want 4"
