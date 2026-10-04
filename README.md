# termalator

`tm` is one binary that hosts coding-agent sessions in a background server and gives you a dashboard. It also runs projects in which a **coordinator** agent is your single point of contact. The coordinator hands work to **threads**: agents working in git worktrees. All progress lives in markdown under `~/.termalator/projects/<slug>/`, which every session can read, so clearing an agent's context loses nothing.

Claude Code is the first supported agent. Other agents plug in through a manifest; see [docs/SPEC.md §8](docs/SPEC.md#8-agents).

**Status:** pre-alpha. The background server works (milestone M1): it hosts shell sessions that survive closing your terminal. The attach client, agents and projects come next; see the [v0.1 specification](docs/SPEC.md).

## Try it

```sh
make run
```

This builds `bin/tm`, starts the background server, starts a session running your shell, types a line into it and prints its screen. The session keeps running after the command returns, and after you close the terminal:

```sh
bin/tm session list                    # sessions in the server
bin/tm session keys s-1 --enter 'ls'   # type into one
bin/tm session read s-1                # print its screen
bin/tm session stop s-1
bin/tm server status | stop
```

`make run RUN_ARGS=top` runs another command instead of your shell. Until the attach client lands (M2), `tm session read` is how you see a session. The server keeps its state in `~/.termalator`; set `TERMALATOR_HOME` to use somewhere else.

## Requirements

macOS or Linux. To build you need:

| Tool | Version | Why |
|---|---|---|
| Go | 1.26 or later | the floor set by `go.mitchellh.com/libghostty` |
| pkg-config | any | the libghostty bindings find the library through it (`brew install pkgconf`, `apt install pkg-config`) |
| git, curl | any | fetch Ghostty at a pinned commit, and Zig if needed |
| a C toolchain | — | cgo (Xcode Command Line Tools on macOS, `build-essential` on Linux) |

`make toolchain` checks all of these. Zig 0.16, which builds libghostty-vt, is fetched into `.build/` automatically: the pinned release, checked against its sha256, for macOS and Linux on arm64 and x86_64. A `zig` 0.16.x on your `PATH` is used instead, and `make ZIG=/path/to/zig` overrides both. Other Zig versions are skipped, because Zig's build API changes between minor releases.

## Build

```sh
make            # fetch Zig if needed and Ghostty, build libghostty-vt into .build/, then build bin/tm
make vet
./bin/tm selftest
```

The first build fetches Ghostty and compiles libghostty-vt, which takes about 30–60 s. Later builds reuse `.build/`. Nothing is installed system-wide. `tm` links libghostty-vt statically and needs only libc at runtime.

- **Plain `go` commands or gopls:** run `eval "$(make env)"` first, so pkg-config finds the library.

## Tests

```sh
make test       # go test -race ./... (unit, integration, fuzz seed corpora)
make e2e-smoke  # the core end-to-end scenarios (every PR in CI)
make e2e        # every end-to-end scenario (nightly in CI)
make fuzz       # every fuzz target, FUZZTIME=5m each (nightly in CI); e.g. make fuzz FUZZTIME=20s
```

`internal/e2e` is the end-to-end harness that every milestone adds scenarios to. A scenario gets an isolated installation (its own `TERMALATOR_HOME` and a short socket path), runs the real `tm`, and looks at what a user would see. Sessions are read through the server; a virtual terminal (a PTY whose output libghostty-vt parses) plays the user's terminal window. The harness can type into that window, kill its client and close it. Screens can be compared with golden files in `internal/e2e/testdata/`; `make e2e E2E_FLAGS=-update` rewrites them. The M1 scenarios cover these cases: the server survives a killed client and a closed terminal, stale sockets, crash detection, a hung server, and the version handshake.

### Why libghostty-vt, and why these bindings

The server keeps a real terminal emulator for every pane. It uses the emulator for state detection and for repainting a pane when you reattach. libghostty-vt is Ghostty's emulator; herdr and tuios use it as well. It has the correctness that agent TUIs need: inline redraw, the kitty keyboard protocol, reflow, and a snapshot API.

We use Mitchell Hashimoto's [go.mitchellh.com/libghostty](https://github.com/mitchellh/go-libghostty) bindings instead of writing our own:

- they track libghostty's C API, which is still changing;
- they already cover the terminal, formatter, snapshot, render state, and the key and mouse encoders.

The bindings' API is not stable either. That is why `internal/emu` is the only package that imports them, and why the Ghostty commit in the `Makefile` (`GHOSTTY_REV`) is pinned to the one the bindings are developed against. Bump the two together.

## Layout

```
cmd/tm/              entry point
internal/server      background server: lifecycle, socket, sessions.json; also the client side
internal/session     one hosted process: PTY + libghostty-vt emulator + attach subscribers
internal/proto       wire protocol: handshake, control NDJSON, attach frames
internal/e2e         end-to-end test harness and scenarios
scripts/run.sh       what `make run` does
internal/agent       agent interface, manifests (manifests/claude.toml), registry
internal/…           see docs/SPEC.md §2.1
docs/SPEC.md         the v0.1 specification and milestone plan
spikes/              throwaway experiments, each with its own go.mod and FINDINGS.md
```

## Licence

[MIT](LICENSE) © 2026 Clifmeister.
