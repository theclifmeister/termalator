# Contributing

terminatr is pre-alpha and changes quickly; [docs/SPEC.md](docs/SPEC.md) is the plan it follows. Issues and pull requests are welcome. For anything larger than a fix, open an issue first so we can agree on the approach before you write the code.

## Building and testing

The tools you need are listed [below](#requirements). Then:

```sh
make            # libghostty-vt into .build/, then bin/tm
make vet        # go vet and staticcheck
make vet-cross  # go vet for windows, darwin and freebsd (no cgo, seconds)
make test       # unit and integration tests
make test-race  # the same with -race (CI runs it on main)
make e2e-smoke  # the end-to-end smoke set that CI runs on every PR; E2E_SHARD=1/2 runs half of it
```

For plain `go` commands or gopls, run `eval "$(make env)"` first. `make test-claude` runs the end-to-end suite against a real, logged-in Claude Code. It costs a few cents and CI doesn't run it, so run it yourself when you change how tm drives Claude. `make test-codex` does the same against a real Codex logged in with ChatGPT; run it when you change how tm drives Codex, or after a Codex update.

## Pull requests

- Keep a PR to one change, with tests: a unit test for logic, an `internal/e2e` scenario for anything a user sees.
- OS code (`syscall`, `x/sys`, `creack/pty`, `runtime.GOOS`) goes in a package under `internal/plat`, never elsewhere: `internal/plat/boundary_test.go` fails on a new site outside it. Its allow-list holds the sites from before the rule, and only shrinks.
- Run `gofmt`; CI is kept lean (GitHub bills every job). A pull request runs one Ubuntu job, `test`: gofmt, `make build`, `make vet`, `make vet-cross`, `make test` (no `-race`), `selftest` and the e2e smoke set; a failure uploads the screens and logs as `e2e-artifacts-linux`. A change to only `*.md` or `docs/` skips it. A newer push to the PR cancels the run in flight. A push to main runs `test` with `make test-race` for the unit tests (the e2e smoke stays without `-race`) and a `macos` job (build, `make test`, smoke set); release tags wait for it, and pull requests never run macOS. The release snapshot runs on a pull request only when it changes release files (`.goreleaser.yaml`, `Makefile`, `go.mod`, `scripts/release/`, `Formula/`, the workflows, `internal/emu`, `internal/version`, `cmd/tm/main.go`); the `claude-mod` job (`claude plugin validate` and `test` on terminatr's mod) likewise only when it changes `internal/agent/claude/`. The weekly workflow (`weekly.yml`, also by hand; it skips when main hasn't changed since its last successful run) runs `make test-race`, the whole e2e set and a race-built smoke set on Ubuntu and macOS, and the fuzzers (1 minute per target, about 20 minutes). The release snapshot moved off main pushes: the release workflow runs it on every tag, before anything is signed or uploaded.
- End-to-end scenarios act only on a settled screen: wait for the text or state you expect (the harness's waits keep the last whole frame), and for a save or a `working…` note to clear, before the next key or click; never a fixed sleep. A flaky test is fixed at its cause (a wait, or a real bug), never retried or skipped. To find one, run it many times under load (`go test -c`, then several copies with `-test.count`, plus CPU hogs) and replay the failing run's `window-N.raw` frame by frame.
- If you change behaviour that docs/SPEC.md or the README describes, update them in the same PR.
- Don't commit machine-specific paths, logs or screen captures that show your home directory or account.

By contributing, you agree that your contributions are licensed under the [MIT licence](LICENSE).

## Security issues

Don't open a public issue for a vulnerability; see [SECURITY.md](SECURITY.md).

## Reference

### Requirements

To build you need:

| Tool | Version | Why |
|---|---|---|
| Go | 1.26 or later | the floor set by `go.mitchellh.com/libghostty` |
| pkg-config | any | the libghostty bindings find the library through it (`brew install pkgconf`, `apt install pkg-config`) |
| git, curl | any | fetch Ghostty at a pinned commit, and Zig if needed |
| a C toolchain | — | cgo (Xcode Command Line Tools on macOS, `build-essential` on Linux) |

`make toolchain` checks all of these. Zig 0.16, which builds libghostty-vt, is fetched into `.build/` automatically: the pinned release, checked against its sha256, for macOS and Linux on arm64 and x86_64. A `zig` 0.16.x on your `PATH` is used instead, and `make ZIG=/path/to/zig` overrides both. Other Zig versions are skipped, because Zig's build API changes between minor releases.

### Build

```sh
make            # fetch Zig if needed and Ghostty, build libghostty-vt into .build/, then build bin/tm
make vet
./bin/tm selftest
```

The first build fetches Ghostty and compiles libghostty-vt, which takes about 30–60 s. Later builds reuse `.build/`. Nothing is installed system-wide. `tm` links libghostty-vt statically and needs only libc at runtime.

- **Plain `go` commands or gopls:** run `eval "$(make env)"` first, so pkg-config finds the library. In a linked worktree without a `.build/` of its own, `make` and `make env` use the main checkout's.

### Tests

```sh
make test       # go test ./... (unit, integration, fuzz seed corpora)
make test-race  # the same with -race (main and weekly in CI)
make e2e-smoke  # the core end-to-end scenarios (every PR in CI)
make e2e-smoke-race  # the same with tm built with -race (main and weekly in CI)
make e2e        # every end-to-end scenario (weekly in CI); E2E_RACE=1 for a race-built tm
make fuzz       # every fuzz target, FUZZTIME=5m each (weekly in CI, 1m each); e.g. make fuzz FUZZTIME=20s
```

On Windows the scenarios run from a cross-built bundle (docs/SPEC.md §16.2): on the Mac, `scripts/e2e-windows.sh arm64 OUT` (amd64 for x64), copy OUT to the Windows box, and there, with Git for Windows installed (its `cmd` on `PATH`):

```powershell
$env:E2E = '1'; $env:E2E_BIN = "$PWD\bin"
.\e2e.test.exe '-test.run=^TestSmoke' '-test.timeout=15m'   # PowerShell needs the flags quoted
```

`internal/e2e` is the end-to-end harness that every milestone adds scenarios to. A scenario gets an isolated installation (its own `TERMINATR_HOME` and a short socket path), runs the real `tm`, and looks at what a user would see. Sessions are read through the server; a virtual terminal (a PTY whose output libghostty-vt parses) plays the user's terminal window. The harness can type into that window, kill its client and close it. Screens can be compared with golden files in `internal/e2e/testdata/`; `make e2e E2E_FLAGS=-update` rewrites them. The M1 scenarios cover these cases: the server survives a killed client and a closed terminal, stale sockets, crash detection, a hung server, and the version handshake.

#### Why libghostty-vt, and why these bindings

The server keeps a real terminal emulator for every pane. It uses the emulator for state detection and for repainting a pane when you reattach. libghostty-vt is Ghostty's emulator; herdr and tuios use it as well. It has the correctness that agent TUIs need: inline redraw, the kitty keyboard protocol, reflow, and a snapshot API.

We use Mitchell Hashimoto's [go.mitchellh.com/libghostty](https://github.com/mitchellh/go-libghostty) bindings instead of writing our own:

- they track libghostty's C API, which is still changing;
- they already cover the terminal, formatter, snapshot, render state, and the key and mouse encoders.

The bindings' API is not stable either. That is why `internal/emu` is the only package that imports them, and why the Ghostty commit in the `Makefile` (`GHOSTTY_REV`) is pinned to the one the bindings are developed against. Bump the two together. Dependabot (`.github/dependabot.yml`) keeps the other Go modules and the GitHub Actions current with weekly PRs; it skips the bindings for this reason.

### Layout

```
cmd/tm/              entry point
internal/server      background server: lifecycle, socket, sessions.json, the views; also the client side
internal/view        the server-owned view: layout tree, actions, geometry
internal/session     one hosted process: PTY + libghostty-vt emulator + attach subscribers
internal/proto       wire protocol: handshake, control NDJSON, attach frames
internal/emu         the libghostty-vt wrapper: emulator, snapshots, renderer, input encoders
internal/tui         the dashboard and the attach client, both screens of a view
internal/e2e         end-to-end test harness and scenarios
internal/agent       agent interface, manifests (manifests/claude.toml), registry
internal/…           see [docs/SPEC.md §2.1](docs/SPEC.md)
scripts/run.sh       what `make run` does
docs/SPEC.md         the v0.1 specification and milestone plan
docs/OPERATIONS.md   installing, state, logs, doctor, service, upgrading, uninstalling
docs/research/       findings of the early spikes (libghostty, Claude Code, symlinks)
scripts/release/     release builds: per-target libghostty-vt, zig cc wrapper, signing, archive checks, formula
Formula/             the Homebrew formula (rewritten by each release)
```
