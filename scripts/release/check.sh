#!/bin/sh
# Checks the release archives in a goreleaser dist/ folder: checksums,
# contents, what each binary links, the glibc floor of the Linux
# binaries, and `tm selftest` for the host's own archive.
#
#   scripts/release/check.sh [--signed] [dist]
#
# --signed (the release workflow, on macOS) also requires each darwin
# binary to carry a Developer ID signature with the hardened runtime and
# to be notarised: Gatekeeper's own assessment must say "Notarized
# Developer ID". `spctl --type execute` rejects every bare binary ("does
# not seem to be an app"), so the check asks for the primary signature
# as `--type open` does for a downloaded file.
set -eu
signed=
if [ "${1:-}" = --signed ]; then
	signed=1
	shift
fi
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
		# Only libraries every Mac has: /usr/lib and system frameworks.
		# Which ones depends on the Go release (Go 1.26 on arm64 adds
		# CoreFoundation and libobjc); anything else (Homebrew, a
		# dynamic libghostty) would break on a clean machine.
		libs=$(otool -L "$d/tm" | tail -n +2 | awk '{print $1}' | grep -v -e '^/usr/lib/' -e '^/System/Library/Frameworks/' || true)
		[ -z "$libs" ] || fail "$name links non-system libraries: $libs"
	fi
	if [ "$os" = darwin ] && [ -n "$signed" ]; then
		v=$(codesign --verify --strict "$d/tm" 2>&1) || fail "$name: signature: $v"
		info=$(codesign -d --verbose=2 "$d/tm" 2>&1)
		echo "$info" | grep -q '^Authority=Developer ID Application' || fail "$name: not signed with a Developer ID"
		echo "$info" | grep -q 'flags=.*runtime' || fail "$name: no hardened runtime"
		gk=$(spctl --assess --type open --context context:primary-signature -v "$d/tm" 2>&1) ||
			fail "$name: Gatekeeper rejects it: $gk"
		echo "$gk" | grep -q 'source=Notarized Developer ID' || fail "$name: not notarised: $gk"
		echo "$name: $(echo "$info" | grep '^TeamIdentifier='), notarised"
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
