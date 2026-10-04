# termalator

`tm` is one binary that hosts coding-agent sessions in a background server and gives you a dashboard. It also runs projects in which a **coordinator** agent is your single point of contact. The coordinator hands work to **threads**: agents working in git worktrees. All progress lives in markdown under `~/.termalator/projects/<slug>/`, which every session can read, so clearing an agent's context loses nothing.

Claude Code is the first supported agent. Other agents plug in through a manifest; see [docs/SPEC.md §8](docs/SPEC.md#8-agents).

**Status:** pre-alpha. The repository has the skeleton and the [v0.1 specification](docs/SPEC.md). `tm` itself only implements `version` and `selftest` so far. `selftest` shows that the terminal emulator is linked.

## Requirements

macOS or Linux. To build you need:

| Tool | Version | Why |
|---|---|---|
| Go | 1.26 or later | the floor set by `go.mitchellh.com/libghostty` |
| Zig | **0.16 or later** | builds libghostty-vt from Ghostty's source |
| pkg-config | any | the libghostty bindings find the library through it (`brew install pkgconf`, `apt install pkg-config`) |
| git | any | fetches Ghostty at a pinned commit |
| a C toolchain | — | cgo (Xcode Command Line Tools on macOS, `build-essential` on Linux) |

`make toolchain` checks all of these.

## Build

```sh
make            # fetch Ghostty, build libghostty-vt into .build/, then build bin/tm
make test       # go test ./...
make vet
./bin/tm selftest
```

The first build fetches Ghostty and compiles libghostty-vt, which takes about 30–60 s. Later builds reuse `.build/`. Nothing is installed system-wide. `tm` links libghostty-vt statically and needs only libc at runtime.

- **Zig not on your PATH:** use `make ZIG=/path/to/zig`.
- **Plain `go` commands or gopls:** run `eval "$(make env)"` first, so pkg-config finds the library.

### Why libghostty-vt, and why these bindings

The server keeps a real terminal emulator for every pane. It uses the emulator for state detection and for repainting a pane when you reattach. libghostty-vt is Ghostty's emulator; herdr and tuios use it as well. It has the correctness that agent TUIs need: inline redraw, the kitty keyboard protocol, reflow, and a snapshot API.

We use Mitchell Hashimoto's [go.mitchellh.com/libghostty](https://github.com/mitchellh/go-libghostty) bindings instead of writing our own:

- they track libghostty's C API, which is still changing;
- they already cover the terminal, formatter, snapshot, render state, and the key and mouse encoders.

The bindings' API is not stable either. That is why `internal/emu` is the only package that imports them, and why the Ghostty commit in the `Makefile` (`GHOSTTY_REV`) is pinned to the one the bindings are developed against. Bump the two together.

## Layout

```
cmd/tm/              entry point
internal/server      background server: PTYs, emulators, agents, socket
internal/agent       agent interface, manifests (manifests/claude.toml), registry
internal/…           see docs/SPEC.md §2.1
docs/SPEC.md         the v0.1 specification and milestone plan
spikes/              throwaway experiments, each with its own go.mod and FINDINGS.md
```

## Licence

[MIT](LICENSE) © 2026 Clifmeister.
