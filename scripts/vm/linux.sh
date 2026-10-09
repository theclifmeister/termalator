#!/usr/bin/env bash
# Prepare the "Ubuntu 26.04 ARM64" Parallels VM for terminatr testing.
# Run from the Mac:   scripts/vm/linux.sh [setup|sync|test]
#   setup  install the build tools, Claude Code and Codex (idempotent)
#   sync   push this checkout's HEAD into ~/tm-test/terminatr in the VM
#   test   build tm, run tm doctor and the unit tests in the VM
# See docs/VMS.md. Without --current-user, prlctl exec runs as root in the
# guest, which is how apt works without a sudo password.
set -euo pipefail
VM="Ubuntu 26.04 ARM64"
root() { prlctl exec "$VM" "$@"; }
user() { prlctl exec "$VM" --current-user "$@"; }
ENVP='export PATH=$HOME/.local/bin:/usr/local/go/bin:$PATH'

setup() {
  root 'DEBIAN_FRONTEND=noninteractive apt-get update -qq &&
    DEBIAN_FRONTEND=noninteractive apt-get install -y -qq git pkg-config build-essential \
      socat bubblewrap curl xz-utils ca-certificates nodejs npm gh'
  # Go: the newest 1.26.x (go.mod says 1.26.0). Zig is fetched by make.
  root 'v=$(curl -fsSL "https://go.dev/dl/?mode=json" | grep -o "go1\.26\.[0-9]*" | sort -V | tail -1) &&
    rm -rf /usr/local/go && curl -fsSL https://go.dev/dl/$v.linux-arm64.tar.gz | tar -C /usr/local -xzf - &&
    npm i -g @openai/codex &&
    chmod -R go+rX /usr/local/go /usr/local/lib/node_modules'
  user 'curl -fsSL https://claude.ai/install.sh | bash'
  # exec sessions have a bare PATH (/bin:/usr/bin); link the tools into it.
  root 'ln -sf /usr/local/go/bin/go /usr/bin/go; ln -sf /usr/local/go/bin/gofmt /usr/bin/gofmt
    ln -sf /usr/local/lib/node_modules/@openai/codex/bin/codex.js /usr/bin/codex
    ln -sf /home/parallels/.local/bin/claude /usr/bin/claude'
  user 'mkdir -p ~/tm-test'
  echo "kernel.apparmor_restrict_unprivileged_userns = $(user 'sysctl -n kernel.apparmor_restrict_unprivileged_userns')"
}

# The Parallels share (/media/psf/Home) can hang on the Mac's privacy
# prompt, so the bundle goes in over stdin instead.
sync() {
  local b; b=$(mktemp -d)/tm.bundle
  git bundle create "$b" HEAD
  user 'cat > ~/tm-test/tm.bundle' < "$b"
  user 'cd ~/tm-test && rm -rf terminatr && git clone -q tm.bundle terminatr'
  rm -rf "$(dirname "$b")"
}

test_() {
  user "$ENVP; cd ~/tm-test/terminatr && make && ./bin/tm doctor; make test"
}

case "${1:-setup}" in
  setup) setup ;;
  sync) sync ;;
  test) test_ ;;
  *) echo "usage: $0 [setup|sync|test]" >&2; exit 2 ;;
esac
