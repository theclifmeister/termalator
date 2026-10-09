#!/usr/bin/env bash
# Prepare the "macOS" Parallels VM for terminatr testing.
# Run from the Mac:   scripts/vm/macos.sh [setup|sync|test]
#   setup  install Xcode command line tools, Homebrew, Go, Claude Code, Codex
#   sync   push this checkout's HEAD into ~/tm-test/terminatr in the VM
#   test   build tm, run tm doctor and the unit tests in the VM
# Needs Parallels Tools in the guest and a logged-in user. See docs/VMS.md.
#
# Scripts go in over stdin (`bash -s`): prlctl strips the quotes from a
# quoted argument. Without --current-user the command runs as root, which
# is how the command line tools and /opt/homebrew get set up with no sudo
# password.
set -euo pipefail
VM="macOS"
GUSER="${GUSER:-clifford}"
root() { prlctl exec "$VM" /bin/bash -s; }
user() { { echo 'umask 022'; cat; } | prlctl exec "$VM" --current-user /bin/bash -s; }
# Exec sessions run with umask 0000, which made ~/.local/* mode 777.
ENVP='export PATH=$HOME/.local/bin:/opt/homebrew/bin:$PATH HOMEBREW_NO_AUTO_UPDATE=1 HOMEBREW_NO_ANALYTICS=1'

setup() {
  # Xcode command line tools. A long softwareupdate dies with the exec
  # session, so it runs as a launchd job and is polled.
  root <<'SH'
if ! xcode-select -p >/dev/null 2>&1; then
  touch /tmp/.com.apple.dt.CommandLineTools.installondemand.in-progress
  l=$(softwareupdate -l 2>&1 | sed -n 's/^\* Label: \(Command Line Tools.*\)/\1/p' | tail -1)
  launchctl remove cltinstall 2>/dev/null
  launchctl submit -l cltinstall -o /tmp/clt.log -e /tmp/clt.log -- /usr/sbin/softwareupdate -i "$l"
fi
SH
  until echo 'xcode-select -p >/dev/null 2>&1 && /usr/bin/git --version >/dev/null 2>&1 && echo DONE' | root | grep -q DONE; do sleep 15; done
  # Homebrew into /opt/homebrew (the installer wants sudo; this does not).
  root <<SH
mkdir -p /opt/homebrew && chown -R $GUSER:admin /opt/homebrew
SH
  user <<SH
set -e
cd /opt/homebrew
[ -x bin/brew ] || curl -fsSL https://github.com/Homebrew/brew/tarball/main | tar xz --strip-components 1
$ENVP
brew install go@1.26 git pkgconf node gh
brew list go >/dev/null 2>&1 && brew uninstall go
brew link --force go@1.26
mkdir -p ~/tm-test ~/.local
npm i -g --prefix ~/.local @openai/codex
curl -fsSL https://claude.ai/install.sh | bash
SH
  # Login shells (what Terminal opens) need the tools on PATH too, and the
  # ~/.local folders must be 755 (fixes ones made 777 by an earlier run).
  user <<'SH'
mkdir -p ~/.local/bin
chmod 755 ~/.local ~/.local/bin ~/.local/lib ~/.local/share ~/.local/state 2>/dev/null || true
chmod 755 ~/.local/share/claude 2>/dev/null || true
l='export PATH="$HOME/.local/bin:/opt/homebrew/bin:$PATH" # terminatr-vm'
grep -qF '# terminatr-vm' ~/.zprofile 2>/dev/null || echo "$l" >> ~/.zprofile
SH
}

sync() {
  local b; b=$(mktemp -d)/tm.bundle
  git bundle create "$b" HEAD
  prlctl exec "$VM" --current-user /usr/bin/tee "/Users/$GUSER/tm-test/tm.bundle" < "$b" >/dev/null
  user <<'SH'
cd ~/tm-test && rm -rf terminatr && git clone -q tm.bundle terminatr
SH
  rm -rf "$(dirname "$b")"
}

test_() {
  user <<SH
$ENVP
cd ~/tm-test/terminatr && make && ./bin/tm doctor; make test
SH
}

case "${1:-setup}" in
  setup) setup ;;
  sync) sync ;;
  test) test_ ;;
  *) echo "usage: $0 [setup|sync|test]" >&2; exit 2 ;;
esac
