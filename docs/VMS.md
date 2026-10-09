# Test VMs (Parallels)

> Contributor notes: how terminatr itself is tested. Not needed to use it.

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
    echo 'script' | prlctl exec macOS --current-user /bin/bash -s

On macOS prlctl strips the quotes from a quoted argument (`sh -c '...'`
fails with PrlJob_GetRetCode), so pass scripts over stdin. Root exec also
works there; the setup uses it for the command line tools and `/opt/homebrew`
instead of a sudo password. Long jobs die with the exec session: run them as
a `launchctl submit` job.

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

macOS (user `clifford`): Xcode command line tools, Homebrew (`/opt/homebrew`),
go@1.26 (as CI), git, pkgconf, node, gh,
Claude Code, Codex. `terminatr` itself is not installed, so a clean
`brew install terminatr` can be tested.

Log in to the agents (`claude`, `codex`) inside the VM when a live test needs
it; live model calls only when a test needs them.

## Login PATH and permissions

`setup` appends one marked line (`# terminatr-vm`) to `~/.zprofile` (macOS) or
`~/.profile` (Linux) putting `~/.local/bin` (and `/opt/homebrew/bin` on macOS)
on PATH, so a login shell finds `claude` and `codex`. It is added once.

Exec sessions run with a loose umask (0000 on macOS, 007 on Linux), which made
`~/.local`, `~/.local/bin` etc. mode 777/770. The scripts now run user commands
with `umask 022` and `chmod 755` those folders on every `setup`, so re-running
`setup` repairs an older VM.

## Known

Linux: the VM keeps Ubuntu's default `kernel.apparmor_restrict_unprivileged_userns = 1`
on purpose, since that is what most users run (CI sets it to 0). So
bubblewrap cannot create user namespaces and `tm doctor` warns about
user namespaces/bwrap, and that a Codex coordinator's sandbox doesn't hold.
These warnings are expected there. Don't change the sysctl.
