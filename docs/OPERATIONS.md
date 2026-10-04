# Operating termalator

How to install, run, check, upgrade and remove `tm`. The design behind all of this is in [SPEC.md](SPEC.md) §3 (server) and §5 (files).

## Install

Release archives for macOS and Linux on arm64 and x86_64 are on the [releases page](https://github.com/theclifmeister/termalator/releases). Each holds one static `tm` binary (plus this file, the README and the licence); `checksums.txt` lists their sha256. To install the latest into `~/.local/bin`:

```sh
mkdir -p ~/.local/bin && curl -fsSL "https://github.com/theclifmeister/termalator/releases/latest/download/tm_$(uname -s | tr A-Z a-z)_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz" | tar -xz -C ~/.local/bin tm
tm doctor
```

- **macOS:** 13 or later. The binary links only system libraries (libSystem, libresolv and, depending on the Go release, CoreFoundation). It is signed ad hoc, not notarised: installed with curl it runs as is; downloaded with a browser, clear the quarantine flag first (`xattr -d com.apple.quarantine tm`).
- **Linux:** glibc 2.28 or later (Debian 10, Ubuntu 18.10, RHEL 8 and newer); musl is not supported.
- **Runtime:** git, and the agents you use (Claude Code). For threads' sandbox Claude needs `bwrap` and `socat` on Linux. `tm doctor` checks all of these.

To build from source instead, see the [README](../README.md#build). Releases are cut by pushing a `v*` tag: `.github/workflows/release.yml` runs goreleaser (`.goreleaser.yaml`, cross-compiling every target with `zig cc` against its own libghostty-vt) and creates a draft release to review and publish. `make release-snapshot` builds the same archives into `dist/` locally, without a tag.

## Where state lives

Everything is under `~/.termalator`, or `$TERMALATOR_HOME` when that is set. Nothing termalator-owned is written into your repositories or thread worktrees.

| Path | What |
|---|---|
| `config.toml` | your settings: default agent, keys, per-project safety |
| `agents/<name>.toml` | your own agent manifests (they override the built-in ones) |
| `projects/<slug>/` | one folder per project: `PROJECT.md`, `TASKS.md`, `JOURNAL.md`, `inbox/`, `threads/<id>/` (brief, reports, status), memory |
| `worktrees/<slug>/<id>-…/` | thread worktrees: plain git checkouts of the project's repos |
| `state/sessions.json` | the sessions the server runs, rewritten on every change; the next server resumes agents from it |
| `logs/server.log` | the server log, rotated at 10 MB (`server.log.1` … `.3` kept) |
| `logs/service.log` | output of a server started by launchd (macOS service only) |
| `run/` | `tm.sock`, `server.lock`, `server.pid` and per-session runtime dirs (`s/<id>/`) |

The run directory is `$XDG_RUNTIME_DIR/termalator` on Linux when that variable is set (and `TERMALATOR_HOME` isn't), and falls back to `/tmp/termalator-<uid>-<hash>` when the path to the socket would be too long. `tm server status` and `tm doctor` print the one in use. `$TERMALATOR_SOCKET` overrides the socket path.

Agents keep their own files too: Claude Code stores conversations under `~/.claude/`, which is what resume uses.

## The server

Any `tm` command starts the server when it isn't running, so normally you don't start it yourself. It runs detached from your terminal: closing the window, the client or an SSH session never stops it or its agents.

```sh
tm server status          # pid, uptime, version, sessions; what the last restart resumed or lost
tm server stop            # asks first while agents run; --yes to skip
tm server restart         # stop, start, resume the agents
tm server stop --force    # a hung server: SIGKILL the pid that holds the lock
```

### Crashes and restarts

When the server stops, every session it hosts stops with it. The next server reads `state/sessions.json` and:
- resumes every coordinator and thread whose agent has worked on a prompt, with the agent's latest session id (`claude --resume <id>`), in the same directory with the same brief and hooks;
- starts an agent that never got a prompt fresh (there is no conversation to resume);
- does not restore shell sessions; they are listed as lost.

Turns that were running are lost; resumed agents are idle. Each affected project gets a `server` inbox item such as `server restarted after crash; resumed coordinator, t-0003; lost shell s-12`, and the coordinator decides what to re-prompt. `tm server status` shows the same.

### Start at login (optional)

Any `tm` command starts the server when needed, so you don't need a service. If you want the server up from login:
- `tm server service install` writes `~/Library/LaunchAgents/dev.termalator.server.plist` and loads it with launchd on macOS (`RunAtLoad`, no `KeepAlive`). On Linux it writes `~/.config/systemd/user/termalator.service` and enables it with `systemctl --user enable --now`.
- The service runs `tm server run` with the `PATH` of the shell you installed it from, so `claude` and `git` are found. Re-run install after moving `tm` or changing `PATH`.
- On macOS the service's own output goes to `~/.termalator/logs/service.log`; the server still logs to `server.log`.
- `tm server service uninstall` unloads and removes the file. That cleanly stops a server the service started, so the next server resumes its agents.
- `--print` shows the file without installing anything.
- On Linux a user service stops at logout unless lingering is on (`loginctl enable-linger`).

## Checking an installation: tm doctor

`tm doctor` checks your installation and changes nothing:
- the `tm` build and libghostty-vt, git and gh;
- the server: running and answering, the same build as this `tm`, a previous crash, stale `tm.sock`, `server.pid` and session runtime dirs (it never starts a server);
- each agent's installed version against its manifest's `tested_versions`. An untested Claude still works, but termalator stops trusting its undocumented status file and messaging socket;
- the sandbox tools Claude needs for threads: `sandbox-exec` on macOS, `bwrap` and `socat` on Linux;
- leftovers: worktrees under `~/.termalator/worktrees` whose thread is resolved or gone, and `tm/<project>/…` branches already merged into the default branch.

It exits 1 only when a check fails; warnings don't count. `--json` prints the results for scripts.

`tm doctor --fix` lists what it would remove and asks before removing anything. `--yes` skips the question, and you need it when you're not on a terminal. Worktrees with uncommitted changes and unmerged branches are always kept. A branch whose PR was squash-merged looks unmerged to git; delete it yourself with `git branch -D`.

## Logs

- `~/.termalator/logs/server.log`: the server's log: starts and stops, sessions, agent state changes, resumes. Start here when something looks wrong.
- `tm agent explain <session>`: why an agent session is in its current state (which signal decided it).
- A server started in the foreground (`tm server run`) logs to stderr instead.

## Upgrading

1. Install the new `tm` over the old one (the same command as installing).
2. `tm server restart`. Until then the old server keeps running the old build: attach still works, because the client re-runs the server's own binary, but `tm doctor` warns that the builds differ and a newer control protocol asks you to restart.

Restart resumes the agents, so an upgrade costs only the turns running at that moment. On a terminal it asks before stopping running agents (`--yes` skips that).

## Uninstalling

```sh
tm server service uninstall   # only if you installed the service
tm server stop --yes
rm "$(command -v tm)"
rm -rf ~/.termalator          # all projects, tasks, reports and thread worktrees: keep a copy if you want them
```

Thread branches (`tm/<project>/…`) live in your repositories and are not removed by this; `tm doctor --fix` before uninstalling deletes the merged ones.
