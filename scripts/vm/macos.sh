#!/usr/bin/env bash
# Prepare the "macOS" Parallels VM for terminatr testing.
# Run from the Mac:   scripts/vm/macos.sh [setup|sync|test]
#   setup  install Xcode command line tools, Homebrew, Go, Claude Code, Codex
#   sync   push this checkout's HEAD into ~/tm-test/terminatr in the VM
#   test   build tm, run tm doctor and the unit tests in the VM
# Needs Parallels Tools in the guest and a logged-in user. See docs/VMS.md.
set -euo pipefail
VM="macOS"
user() { prlctl exec "$VM" --current-user "$@"; }
ENVP='export PATH=$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH'

setup() {
  user 'xcode-select -p >/dev/null 2>&1 || {
    touch /tmp/.com.apple.dt.CommandLineTools.installondemand.in-progress
    p=$(softwareupdate -l | sed -n "s/^\* Label: \(Command Line Tools.*\)/\1/p" | tail -1)
    sudo -n softwareupdate -i "$p" --verbose; }'
  user 'command -v brew >/dev/null || NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"'
  # Go as go.mod/CI; Zig is fetched by make; a clean `brew install terminatr`
  # is tested later, so terminatr itself is not installed here.
  user "$ENVP"'; brew install go git pkgconf node gh'
  user "$ENVP"'; npm i -g --prefix ~/.local @openai/codex'
  user "$ENVP"'; curl -fsSL https://claude.ai/install.sh | bash'
  user 'mkdir -p ~/tm-test'
}

sync() {
  local b; b=$(mktemp -d)/tm.bundle
  git bundle create "$b" HEAD
  user 'cat > ~/tm-test/tm.bundle' < "$b"
  user 'cd ~/tm-test && rm -rf terminatr && git clone -q tm.bundle terminatr'
  rm -rf "$(dirname "$b")"
}

test_() {
  user "$ENVP"'; cd ~/tm-test/terminatr && make && ./bin/tm doctor; make test'
}

case "${1:-setup}" in
  setup) setup ;;
  sync) sync ;;
  test) test_ ;;
  *) echo "usage: $0 [setup|sync|test]" >&2; exit 2 ;;
esac
