# Test VMs (Parallels)

Two Parallels VMs on the Mac host for testing terminatr on Linux and macOS.
Both are set up by a script in `scripts/vm/`, run from the Mac.

| VM | Script | Notes |
|---|---|---|
| `Ubuntu 26.04 ARM64` | `scripts/vm/linux.sh` | user `parallels` (sudo group), kernel 7.0 |
| `macOS` | `scripts/vm/macos.sh` | needs Parallels Tools and a logged-in user |

Each script takes `setup` (install tools), `sync` (put this checkout's HEAD in
`~/tm-test/terminatr`) and `test` (`make`, `tm doctor`, `make test`).

## Access

    prlctl exec "Ubuntu 26.04 ARM64" --current-user 'cmd'   # as user parallels
    prlctl exec "Ubuntu 26.04 ARM64" 'cmd'                  # as root (apt, no password)

`--current-user` is the logged-in user; without it the command runs as root
(Linux) with a bare PATH (`/bin:/usr/bin`), so tools are linked into
`/usr/bin`. `sudo` as `parallels` asks for a password; use the root form
instead. `prlctl` needs to run outside the agent sandbox. One command per
call: a `;`-joined line ending in a failing command returns 255.

## File transfer

The Mac's folders are mounted in the guest (`/media/psf/Home`), but reading
them can hang on the Mac's privacy prompt. Pipe through stdin instead:

    prlctl exec "Ubuntu 26.04 ARM64" --current-user 'cat > ~/tm-test/x' < file

`sync` does this with a `git bundle`, so nothing is left on the Mac.

## Installed

Linux: git, build-essential, pkg-config, socat, bubblewrap, curl, gh, node 22,
Go 1.26.x (`/usr/local/go`), Claude Code (`~/.local/bin`), Codex (npm,
global). Zig 0.16.0 is downloaded by `make` into `.build/`.

macOS: Xcode command line tools, Homebrew, go, git, pkgconf, node, gh,
Claude Code, Codex. `terminatr` itself is not installed, so a clean
`brew install terminatr` can be tested.

Log in to the agents (`claude`, `codex`) inside the VM when a live test needs
it; live model calls only when a test needs them.

## Known

Linux: `kernel.apparmor_restrict_unprivileged_userns = 1` (Ubuntu's default),
so bubblewrap cannot create user namespaces and `tm doctor` warns, including
that a Codex coordinator's sandbox doesn't hold. Left as is, to test the
default; `sysctl kernel.apparmor_restrict_unprivileged_userns=0` (as root)
lifts it.
