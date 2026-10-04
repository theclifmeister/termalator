#!/bin/sh
# Signs and notarises one darwin tm binary in place. goreleaser runs it
# after each build (.goreleaser.yaml, builds.hooks.post), before the
# binary is archived, so the archive holds the signed file:
#
#   scripts/release/sign.sh <path> <goos>
#
# Linux binaries pass through untouched. Without TM_SIGN_IDENTITY (a
# snapshot, a fork, a local `make release-snapshot`) the binary keeps the
# ad-hoc signature zig's linker gave it, and the script says so.
#
#   TM_SIGN_IDENTITY  "Developer ID Application: Name (TEAMID)"; set by the
#                     release workflow from the imported certificate
#   TM_NOTARY_KEY     path to the App Store Connect API key (.p8); with
#   TM_NOTARY_KEY_ID  and TM_NOTARY_ISSUER, notarise after signing
#
# A bare Mach-O can't carry a stapled ticket (stapler only staples
# bundles, disk images and installer packages). Apple still records the
# notarisation against the binary's cdhash, so Gatekeeper accepts a
# quarantined copy after an online lookup; curl and Homebrew formulae set
# no quarantine flag, so they never ask (docs/OPERATIONS.md, Install).
set -eu
bin=${1:?usage: sign.sh <path> <goos>}
goos=${2:?usage: sign.sh <path> <goos>}
[ "$goos" = darwin ] || exit 0
name=$(basename "$(dirname "$bin")")

if [ -z "${TM_SIGN_IDENTITY:-}" ]; then
	echo "sign: TM_SIGN_IDENTITY not set; $name stays ad-hoc signed and is not notarised"
	exit 0
fi

# Hardened runtime (notarisation requires it) and a secure timestamp
# (keeps shipped builds valid after the certificate expires). tm uses no
# JIT, no unsigned memory and loads no libraries, so it needs no
# entitlements. The identifier is fixed, not the file name, so every
# release has the same designated requirement.
out=$(codesign --force --sign "$TM_SIGN_IDENTITY" --identifier dev.termalator.tm \
	--options runtime --timestamp "$bin" 2>&1) || { echo "$out" >&2; exit 1; }
codesign --verify --strict --verbose=2 "$bin"
codesign -d --verbose=2 "$bin" 2>&1 | grep -q 'flags=.*runtime' || {
	echo "sign: $bin is not hardened-runtime signed" >&2
	exit 1
}
echo "sign: $name signed with $TM_SIGN_IDENTITY"

if [ -z "${TM_NOTARY_KEY:-}" ]; then
	echo "sign: TM_NOTARY_KEY not set; $name is signed but not notarised"
	exit 0
fi
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# notarytool takes a zip, disk image or package, never a bare binary.
ditto -c -k --keepParent "$bin" "$tmp/tm.zip"
# --wait exits nonzero on anything but Accepted; --timeout keeps a stuck
# submission from using the job's whole budget.
rc=0
xcrun notarytool submit "$tmp/tm.zip" \
	--key "$TM_NOTARY_KEY" --key-id "${TM_NOTARY_KEY_ID:?}" --issuer "${TM_NOTARY_ISSUER:?}" \
	--wait --timeout 30m >"$tmp/notary.log" 2>&1 || rc=$?
cat "$tmp/notary.log"
if [ "$rc" -ne 0 ] || ! grep -q 'status: Accepted' "$tmp/notary.log"; then
	# A rejection says only "Invalid"; the reason is in Apple's log.
	id=$(grep -oE '[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}' "$tmp/notary.log" | head -1)
	[ -z "$id" ] || xcrun notarytool log "$id" \
		--key "$TM_NOTARY_KEY" --key-id "$TM_NOTARY_KEY_ID" --issuer "$TM_NOTARY_ISSUER" || true
	echo "sign: $name was not notarised" >&2
	exit 1
fi
echo "sign: $name notarised"
