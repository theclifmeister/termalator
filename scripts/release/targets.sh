# Release targets as Zig triples. Linux pins the glibc floor: binaries run
# on glibc 2.28 and later (Debian 10, RHEL 8, Ubuntu 18.10).
RELEASE_TARGETS="aarch64-macos x86_64-macos aarch64-linux-gnu.2.28 x86_64-linux-gnu.2.28"

# zig_triple GOOS GOARCH prints the Zig triple for a Go target.
zig_triple() {
	case "$1/$2" in
	darwin/arm64) echo aarch64-macos ;;
	darwin/amd64) echo x86_64-macos ;;
	linux/arm64) echo aarch64-linux-gnu.2.28 ;;
	linux/amd64) echo x86_64-linux-gnu.2.28 ;;
	*) echo "no release target for $1/$2" >&2; return 1 ;;
	esac
}
