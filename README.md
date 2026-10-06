# terminatr

`tm` is one binary that hosts coding-agent sessions in a background server and gives you a dashboard. It also runs projects in which a **coordinator** agent is your single point of contact. The coordinator hands work to **threads**: agents working in git worktrees. All progress lives in markdown under `~/.terminatr/projects/<slug>/`, which every session can read, so clearing an agent's context loses nothing.

Claude Code is the first supported agent. Other agents plug in through a manifest; see [docs/SPEC.md §8](docs/SPEC.md#8-agents).

**Status:** pre-alpha; see the [v0.1 specification](docs/SPEC.md). What works:
- the background server (milestone M1): it hosts shell sessions that survive closing your terminal (`tm server …`, `tm session …`);
- the attach client (milestone M2): `tm attach` shows a session beside the projects sidebar, with a status bar under it; Ctrl+B then `d` detaches and leaves it running;
- agent sessions (milestone M3): `tm session start --agent claude` runs Claude Code with its live state (working / blocked / idle), todos and resume;
- the dashboard (milestone M4): `tm` lists every session and project with live state, attaches with `enter` and comes back with Ctrl+B then `d`; a projects sidebar on the left of every screen is a folder-style project tree, always expanded: each project with its coordinator and threads on tree connectors (threads named by their task, `T12`; state and progress in aligned columns); a click on a project shows its dashboard, on a coordinator or a thread attaches it;
- server-owned views: every `tm` you open, in any terminal, shows the same screen, as tmux's sessions do. Open a session or move the sidebar in one, and the others follow; `tm --own` opens one that keeps to itself;
- projects and tasks (milestone M5): `tm project new|list|open`, `tm skill`, `tm task …`, `tm context` and `tm inbox list|done`;
- threads (milestone M6): `tm thread start|adopt|prompt|answer|restart|resolve`, agents in git worktrees reporting through `tm`, named by their task: `tm thread show T12` is T12's open thread; `tm thread adopt` (or `T` on the dashboard) makes an agent you started yourself a thread;
- hardening (milestone M8): agents resume after a server crash or restart, `tm doctor [--fix]`, an optional login service (`tm server service install`), signed release archives, a Homebrew formula and `tm update`;
- mods: `tm watch [--json]` streams a session's state, task, PR and inbox counts on each change; terminatr's own mod for Claude Code (early access, off by default: Mods in the settings, `,`) reads that feed and shows a band in the agent's pane with the task, its steps, the PR and what waits for you, reports the session's state from Claude's own turn events, so most `tm hook` processes go, and hands queued prompts and slash commands to Claude itself, which runs them once idle without typing over what you half-typed (paste stays the fallback).

## Install

On macOS 13+ or Linux with glibc 2.28+, with Homebrew:

```sh
brew tap theclifmeister/terminatr https://github.com/theclifmeister/terminatr
brew trust --formula theclifmeister/terminatr/terminatr
brew install theclifmeister/terminatr/terminatr
```

Newer Homebrew refuses formulas from a tap it doesn't trust; `brew trust` once after tapping allows this one.

Terminatr was called Termilator up to v0.6.2. To upgrade from that by hand: stop the server (`tm server stop --yes`); with Homebrew, `brew uninstall termilator`, `brew untap theclifmeister/termilator` and tap and install the new one as above; `mv ~/.termilator ~/.terminatr`, edit the paths inside `~/.terminatr/config.toml` and the project files that name `~/.termilator`, then start it again (`tm server start`); details and the login service in [Upgrading from Termilator](docs/OPERATIONS.md#upgrading-from-termilator). Older installs: [Upgrading from Termalator](docs/OPERATIONS.md#upgrading-from-termalator).

or directly, into `~/.local/bin`:

```sh
mkdir -p ~/.local/bin && curl -fsSL "https://github.com/theclifmeister/terminatr/releases/latest/download/tm_$(uname -s | tr A-Z a-z)_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz" | tar -xz -C ~/.local/bin tm
```

Then `tm doctor`. macOS binaries are signed with a Developer ID and notarised. `tm update` installs new releases (with Homebrew it runs `brew upgrade terminatr`). Where state lives, logs, the login service, upgrading and uninstalling: [docs/OPERATIONS.md](docs/OPERATIONS.md). To build from source, see below.

## Try it

```sh
make run
```

This builds `bin/tm`, starts the background server and opens the dashboard. Press `n` to create a project, then `enter` on it to start its coordinator (Claude Code), or `s` for a shell; either attaches right away, with a status bar at the bottom. You talk to the coordinator; it runs the threads, whose panes you can open too (`enter` on a thread) to read along or answer a prompt; typing into one tells its coordinator that you stepped in. Ctrl+B is the prefix key, as in tmux (the UI writes it `prefix+<key>`, e.g. `prefix+d`): Ctrl+B then `d` brings you back to the dashboard (then `p`, `]` or `[` switches project, `i` opens the inbox), where a session that waits for you (a permission dialog, say) shows under NEEDS YOU; `enter` attaches again. `?` lists the keys, Ctrl+B then `q` quits (a prefix command means the same everywhere; popups close with `esc`). Sessions keep running after you quit, and after you close the terminal:

```sh
bin/tm                                 # the dashboard again, or the session it showed
bin/tm --own                           # a console of its own, which no other follows
bin/tm attach s-1                      # attach again, from any terminal, at any size
bin/tm attach                          # the newest session
bin/tm session list                    # sessions in the server
bin/tm session keys s-1 --enter 'ls'   # type into one
bin/tm session read s-1                # print its screen
bin/tm session stop s-1
bin/tm server status | stop
```

`make run RUN_ARGS=top` also starts a session running `top`. Consoles of different sizes show the same screen: the one you type in, or resize, sizes the panes, and a larger one shows the frame padded, a smaller one cropped. A new pane fills the first console that shows it; after that, attaching and watching never resize anything. Shift+PgUp/PgDn scroll back through a shell's output; full-screen programs get the mouse wheel. The prefix key can be changed in the settings (`,` on the dashboard: `enter` on Prefix key, then press the new one, e.g. Ctrl+A); do that when you run `tm` inside tmux, which takes Ctrl+B itself. Ctrl+B twice sends Ctrl+B to the program (Claude Code uses it to background a running task). The projects sidebar is resizable: drag its border, or `{` `}` (Ctrl+B then `{` `}` in a session); `b` makes it a slim strip. It works from the keyboard too: `tab` on the dashboard (Ctrl+B then Tab in a session) moves the keyboard to it, the arrows move through the tree, `enter` opens the row and `esc` goes back. The Icons setting (`,`) picks the sidebar's and the lists' glyphs: Nerd Font icons, plain Unicode or ASCII; auto uses Nerd Font icons in Ghostty and Unicode elsewhere. Everything works with the mouse too: a click selects, a double-click opens, a right-click on a row or the sidebar opens a menu of its actions, the footer's hints and the status bar's buttons (`≡` menu, `prefix+d dashboard`) are buttons, popups take clicks (a click outside closes them, as `esc` does), the wheel scrolls, and the dividers drag; inside a pane, programs that use the mouse (Claude Code does) still get it; a drag over a shell selects text and copies it on release, and Claude Code's own copy is passed on, both to the clipboard of the terminal you sit at (OSC 52, so it works over SSH; tmux needs `set -g set-clipboard on`); Shift-drag (Option-drag in some macOS terminals) still selects natively. When the dashboard has 120 columns or more beside the sidebar the dashboard shows the selected row's details beside the list (`<` `>` resize it, `|` hides it). `a` (Ctrl+B then `a` in a session) opens the project popup: its overview with the repositories, its inbox (read-only), its tasks (where `D`, `A` and `x` ask the coordinator to delegate, accept or send back the selected one; the coordinator changes tasks), its settings, changed in place with `enter` (and `+` / `-` for numbers, such as how many threads may work at once and after how many days a finished thread closes), and every key; `,` has the settings of every project, and its All projects tab the project settings every project follows (a new one too) unless it sets its own: a project's Settings tab marks those `· all projects`, and `x` on one it set itself makes it follow All projects again. In `~/.terminatr/config.toml` they are the `[defaults]` table, with the same keys as `[projects.<slug>]`. The server keeps its state in `~/.terminatr`; set `TERMINATR_HOME` to use somewhere else.

To pick up a coordinator from the Claude desktop or mobile app (Claude Code's Remote Control), turn on Remote control in the project popup's Settings tab (`a`, then `4`), or set `coordinator_remote_control = true` under `[projects.<slug>]` in `~/.terminatr/config.toml`: the coordinator then starts with remote control, listed under the project's slug. Ctrl+B then `r` on the coordinator's pane (or with its project selected on the dashboard), or `tm project remote on|off <slug>`, turns it on or off in the running session, and the conversation continues; that lasts until the coordinator is started anew. The sidebar shows `⌁` on the coordinator's row and the status bar says `remote control on` while it is.

To park a project, `tm project pause <slug>` (or Paused in the popup's Settings tab): its coordinator gets no nudges, its threads no pull request follow-up, and no new thread starts until `tm project resume <slug>`; the sidebar shows `∥` after it. `tm project archive <slug>` hides a finished project from the sidebar and stops all background work for it (`tm project unarchive` brings it back), and `tm project delete <slug>` moves its folder to `~/.terminatr/.trash/`; both refuse while its coordinator or threads run. `tm project rename <slug> <new-slug> [--name "…"]` renames a project's slug and moves its folder and worktrees with it (no thread may run; its coordinator is restarted under the new slug; see [OPERATIONS](docs/OPERATIONS.md#renaming-a-project)). The coordinator can give a thread a smaller or larger model with `--model` on `tm task delegate` / `tm thread start`, from the list `tm context` shows (the agent manifest's `[[models]]`, narrowed by the `models` setting if you limit them: Thread models in the popup, e.g. to leave out haiku, which has no auto mode). When `gh` keeps failing (logged out, keychain refused), the coordinator gets a `gh-failing` inbox item, and `tm doctor` checks `gh auth status`.

## Try projects and tasks

```sh
export TERMINATR_HOME=$(mktemp -d)        # leave ~/.terminatr alone while trying it
./bin/tm project new "Demo" --goal "Try tm"
export TERMINATR_PROJECT=demo             # or cd into $TERMINATR_HOME/projects/demo
./bin/tm task add "Fix login redirect" --step "Reproduce" --step "Fix"
./bin/tm task status T1 started
./bin/tm task steps T1 check 1
./bin/tm task list                         # add --json for machine-readable output
./bin/tm context                           # what the coordinator reads every turn
./bin/tm skill coordinator                 # the coordinator's standing rules, versioned with tm
```

`tm task help` lists every task command. Exit codes: 0 done or already true, 1 refused (with a stable code such as `human-only`), 2 usage error, 3 I/O error.

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

- **Plain `go` commands or gopls:** run `eval "$(make env)"` first, so pkg-config finds the library. In a linked worktree without a `.build/` of its own, `make` and `make env` use the main checkout's.

## Tests

```sh
make test       # go test ./... (unit, integration, fuzz seed corpora)
make test-race  # the same with -race (main and weekly in CI)
make e2e-smoke  # the core end-to-end scenarios (every PR in CI)
make e2e-smoke-race  # the same with tm built with -race (main and weekly in CI)
make e2e        # every end-to-end scenario (weekly in CI); E2E_RACE=1 for a race-built tm
make fuzz       # every fuzz target, FUZZTIME=5m each (weekly in CI, 1m each); e.g. make fuzz FUZZTIME=20s
```

`internal/e2e` is the end-to-end harness that every milestone adds scenarios to. A scenario gets an isolated installation (its own `TERMINATR_HOME` and a short socket path), runs the real `tm`, and looks at what a user would see. Sessions are read through the server; a virtual terminal (a PTY whose output libghostty-vt parses) plays the user's terminal window. The harness can type into that window, kill its client and close it. Screens can be compared with golden files in `internal/e2e/testdata/`; `make e2e E2E_FLAGS=-update` rewrites them. The M1 scenarios cover these cases: the server survives a killed client and a closed terminal, stale sockets, crash detection, a hung server, and the version handshake.

### Why libghostty-vt, and why these bindings

The server keeps a real terminal emulator for every pane. It uses the emulator for state detection and for repainting a pane when you reattach. libghostty-vt is Ghostty's emulator; herdr and tuios use it as well. It has the correctness that agent TUIs need: inline redraw, the kitty keyboard protocol, reflow, and a snapshot API.

We use Mitchell Hashimoto's [go.mitchellh.com/libghostty](https://github.com/mitchellh/go-libghostty) bindings instead of writing our own:

- they track libghostty's C API, which is still changing;
- they already cover the terminal, formatter, snapshot, render state, and the key and mouse encoders.

The bindings' API is not stable either. That is why `internal/emu` is the only package that imports them, and why the Ghostty commit in the `Makefile` (`GHOSTTY_REV`) is pinned to the one the bindings are developed against. Bump the two together. Dependabot (`.github/dependabot.yml`) keeps the other Go modules and the GitHub Actions current with weekly PRs; it skips the bindings for this reason.

## Layout

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
internal/…           see docs/SPEC.md §2.1
scripts/run.sh       what `make run` does
docs/SPEC.md         the v0.1 specification and milestone plan
docs/OPERATIONS.md   installing, state, logs, doctor, service, upgrading, uninstalling
docs/research/       findings of the early spikes (libghostty, Claude Code, symlinks)
scripts/release/     release builds: per-target libghostty-vt, zig cc wrapper, signing, archive checks, formula
Formula/             the Homebrew formula (rewritten by each release)
```

## Licence

[MIT](LICENSE) © 2026 Clifmeister.
