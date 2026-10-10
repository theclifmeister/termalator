# Using Codex

terminatr runs [Codex](https://developers.openai.com/codex) next to Claude Code: as a coordinator, as threads, or both. This page covers what differs. The rest of tm works the same.

## Set up

1. Install Codex and log in with your ChatGPT account (`codex login`). Run `tm doctor` to check that tm finds it.
2. With only Codex installed, tm uses it for coordinators and threads (`codex (auto)`). With Claude Code installed too, opening a project asks once which agent to run, for all projects. To choose per project, use the project's popup: `a` on the project, then **Settings**. **Thread agent** sets the agent for new threads, **Coordinator agent** the one for a new coordinator (a running coordinator keeps its agent until it is started anew). Both can be set for all projects too. In `config.toml` they are `thread_agent` and `coordinator_agent`.
3. For one thread, name the agent instead: `tm thread start --agent codex "title"`.

## Models

tm asks your Codex which models you can use (`codex app-server`, `model/list`), so the list follows your Codex and your login without a tm release. Settings > General > **Models** shows them, and `tm doctor` says when they were last asked. Threads run Codex's own default unless a model is chosen: pick one with `--model` (`tm thread start --agent codex --model <name> …`), or set a default, hide models or add one Codex doesn't list in the Models page. If the project limits its models (**Thread models** in Settings, or `models` in `config.toml`), the list must include the Codex models, or `--model` is refused with `model-not-allowed`.

A Codex coordinator can't switch models while it runs (its `/model` only opens a picker), so a change to **Coordinator model** applies when the coordinator next starts; until then its status bar says `model … on next start`. A Claude coordinator switches at once.

Codex lists every model whatever your plan, so a model your ChatGPT plan doesn't include still shows; Codex says so in the thread's pane when it refuses it. Logged out, tm can't tell which models you have: `tm doctor` says so, threads run Codex's default, and `--model` is refused until you `codex login`.

## Approvals and the sandbox

- A thread asks for approval (`-a on-request`). The coordinator approves the in-scope ones with `tm thread approve`; the rest reach you in the dashboard. A coordinator runs on Codex's own auto-review.
- A thread's sandbox: its worktree is writable; the project folder and `config.toml` are read-only; the network is open (tm's socket needs it, which also opens outbound HTTPS).
- A coordinator's sandbox: the project folder is writable; thread worktrees and `config.toml` are read-only; the network reaches only tm's socket. `gh` and `az` (reading or merging a PR) ask to run outside the sandbox, and auto-review decides. It relies on Codex's experimental network proxy, which tm turns on for the coordinator. Since that feature is experimental, tm checks before each coordinator starts that the sandbox really holds on your Codex. If a newer Codex changed it, the coordinator runs without this sandbox (writes outside the project folder and every network call, `tm` included, ask auto-review), the server log says why, and `tm doctor` warns, naming your Codex version.
- **Coordinator may merge** (`coordinator_merges`) changes nothing for a Codex coordinator: its PR merges still ask to run outside the sandbox and go through Codex's auto-review, on or off. tm doesn't open the PR host (GitHub, Azure DevOps) to the coordinator's sandbox, which would let every `gh` or `az` call through, not just the merge.
- **Windows.** Codex 0.162's Windows sandbox is a restricted token and needs no setup: a thread's commands run in it and can't write outside the workspace (checked on Windows 11 arm64). Codex connects a sandboxed command to an AF_UNIX socket only when the sandbox may write the socket's folder, and `network.unix_sockets` isn't what opens it, so on Windows tm also grants write on the folder of `tm.sock` (the run folder under `%LOCALAPPDATA%\terminatr`; the config file and the project folder stay read-only). `tm doctor` shows `codex thread sandbox`: OK when a command run in a thread's profile reaches tm's socket and can't write outside the workspace, else a warning with Codex's reason. A coordinator's "network reaches only tm's socket" doesn't hold on Windows (Codex's network proxy isn't applied there; `tm doctor` says so and the coordinator runs without that profile). Codex's stronger `[windows] sandbox = "elevated"` mode (dedicated sandbox accounts, firewall rules) asks for administrator approval; tm doesn't need it and hasn't been checked with it.
- A thread or coordinator started in yolo mode has no sandbox.
- The guard covers Codex: a refused command (a PR merge, a write outside the worktree) is denied before any approval.

## What you'll see

- Prompts you or the coordinator send reach Codex through `codex queue`, so they show up as your own prompt in its terminal: at once when it is idle, after the current turn when it is busy. Slash commands can't be sent that way. After `/clear` (`/new`, `/resume`, `/fork`), sent by tm or typed in the pane yourself, prompts are pasted until the new thread's first prompt reports its id, so none goes to the thread you left.
- Usage shows the plan limit used (a percentage) rather than a cost.
- Codex keeps its sessions under `~/.codex`. tm adds nothing there; its settings are passed on the command line.
- If you have your own Codex hooks that are untrusted, tm skips them in its sessions instead of stopping at Codex's review screen.

## Tested versions and drift

tm was last tested with Codex **0.162.0**. That number is information, not a requirement:
- A newer Codex is supported, and `tm doctor` says so beside the version.
- tm doesn't refuse a version. It watches for what a new release changes and falls back where it can.

What tm relies on, and what it does when that changes:
- **Launch flags** (`resume <id>`, `-m`, `-a on-request`, `--approve-for-me`, `--dangerously-bypass-approvals-and-sandbox`). If Codex refuses one, it exits at once. The server log then says `agent drift: codex <version> … exited … after its launch`, with Codex's error.
- **`-c` settings**: update check, folder trust, the brief, the `terminatr` MCP server, the thread's `tm` permission profile, and the hooks with their trust. Codex ignores a setting it doesn't know, so a renamed one isn't noticed in a session; the weekly canary catches it.
- **Hooks** (each one trusted for the session by a hash). If none arrive while Codex works, the log says `no hook event came`. State then comes from the rollout and the screen.
- **`codex queue`**, for prompts. If it fails, the prompt is pasted instead, and the log says so once per session.
- **The rollout** (`~/.codex/sessions/…`): usage and turn ends. If it changes, only usage and that state source are lost.
- **Screen text**: the trust, hooks-review and update screens, the approval and question dialogs, "Implement this plan?", and the composer. If one changes, its dialog may go unseen; the other sources still give the state.

`tm agent explain <session>` lists the session's drift notes, its Codex version and the last tested version. Every week, CI installs the latest Codex and Claude Code and checks the flags, settings, hooks and screen text, with no login and no model calls. If something changed, it opens an `agent-drift` issue. The full list and what to check when bumping the tested version: [SPEC.md §8.8](SPEC.md#88-tested-versions-and-drift).

## For contributors

`make test-codex` runs the end-to-end scenarios against the real `codex` and costs a little usage. Keep its sessions out of `~/.codex` with a home of its own, logged in once:

```sh
CODEX_HOME=~/.codex-tm-test codex login
CODEX_HOME=~/.codex-tm-test make test-codex
```

How it works: [SPEC.md §8.6, Codex](SPEC.md#8-agents).
