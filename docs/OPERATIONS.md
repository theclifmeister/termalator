# Operating terminatr

How to install, run, check, upgrade and remove `tm`. The design behind all of this is in [SPEC.md](SPEC.md) §3 (server) and §5 (files).

## Install

On macOS 13+ or Linux with glibc 2.28+, on arm64 or x86_64.

**Homebrew** (macOS and Linux):

```sh
brew tap theclifmeister/terminatr https://github.com/theclifmeister/terminatr
brew trust --formula theclifmeister/terminatr/terminatr
brew install theclifmeister/terminatr/terminatr
tm doctor
```

The two-argument `brew tap` is needed because the formula lives in this repository (`Formula/terminatr.rb`), not in a `homebrew-terminatr` one. Newer Homebrew refuses to install from a tap it doesn't trust, hence `brew trust --formula` once after tapping. `brew upgrade terminatr` (or `tm update`, which suggests it) picks up new releases.

**Direct download.** Release archives are on the [releases page](https://github.com/theclifmeister/terminatr/releases). Each holds one static `tm` binary (plus this file, the README and the licence); `checksums.txt` lists their sha256. To install the latest into `~/.local/bin`:

```sh
mkdir -p ~/.local/bin && curl -fsSL "https://github.com/theclifmeister/terminatr/releases/latest/download/tm_$(uname -s | tr A-Z a-z)_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz" | tar -xz -C ~/.local/bin tm
tm doctor
```

`tm update` keeps a direct install up to date.

- **macOS:** the binary links only system libraries (libSystem, libresolv and, depending on the Go release, CoreFoundation). It is signed with a Developer ID (hardened runtime) and notarised. A bare binary can't carry a stapled ticket, so Gatekeeper checks the notarisation online the first time it meets a quarantined copy (one a browser downloaded); with no network that first run is refused. curl and Homebrew set no quarantine flag, so they never ask.
- **Linux:** glibc 2.28 or later (Debian 10, Ubuntu 18.10, RHEL 8 and newer); musl is not supported.
- **Runtime:** git, and the agents you use (Claude Code). For threads' sandbox Claude needs `bwrap` and `socat` on Linux. `tm doctor` checks all of these.

To build from source instead, see the [README](../README.md#build). How releases are made: [Releasing](#releasing).

## Where state lives

Everything is under `~/.terminatr`, or `$TERMINATR_HOME` when that is set. Nothing terminatr-owned is written into your repositories or thread worktrees.

| Path | What |
|---|---|
| `config.toml` | your settings: the default agent, the prefix key (`[keys]`), the icons (`[ui]`), terminatr's mod for Claude (`[mods] enabled`, `band`, `pane`), and the project settings for all projects (`[defaults]`) and per project (`[projects.<slug>]`, which wins key by key). The settings popups (`,` and a project's Settings tab) write it; so do `tm project pause`, `archive`, `delete` and `rename` |
| `ui.json` | this console's layout: the details panel, the list width, and the sidebar and info panel widths new views start with |
| `agents/<name>.toml` | your own agent manifests (they override the built-in ones) |
| `projects/<slug>/` | one folder per project: `PROJECT.md`, `CONTEXT.md`, `MEMORY.md` and `memory/`, `TASKS.md`, `JOURNAL.md`, `inbox/`, `threads/<id>/` (brief, reports, status, attached files), `uploads/` |
| `.trash/<slug>-<time>/` | projects removed with `tm project delete`; tm never empties it |
| `worktrees/<slug>/<id>-…/` | thread worktrees: plain git checkouts of the project's repos |
| `state/sessions.json` | the sessions the server runs, rewritten on every change; the next server resumes agents from it |
| `state/views.json` | what the shared consoles show (screen, session, sidebar), restored after a restart |
| `state/ticker.json` | what the ticker already reported (thread states, PRs, nudges, checkout syncs), so a restart repeats nothing |
| `logs/server.log` | the server log, rotated at 10 MB (`server.log.1` … `.3` kept) |
| `logs/service.log` | output of a server started by launchd (macOS service only) |
| `run/` | `tm.sock`, `server.lock`, `server.pid` and per-session runtime dirs (`s/<id>/`: the agent's generated settings and plugin, with terminatr's mod when it is on) and `bin/tm`: the running server's own copy of its binary, which it runs from, so `tm update` or `brew upgrade` can't pull it from under the server and macOS privacy settings see one `tm` across releases ([macOS privacy prompts](#macos-privacy-prompts)); per-build `bin/tm-<build>` copies and a `server-bin/` from older versions are removed on start |

The run directory is `$XDG_RUNTIME_DIR/terminatr` on Linux when that variable is set (and `TERMINATR_HOME` isn't), and falls back to `/tmp/terminatr-<uid>-<hash>` when the path to the socket would be too long. `tm server status` and `tm doctor` print the one in use. `$TERMINATR_SOCKET` overrides the socket path.

Agents keep their own files too: Claude Code stores conversations under `~/.claude/`, which is what resume uses.

### Renaming a project

`tm project rename <slug> <new-slug> [--name "…"]` renames a project's slug, the name of its folder (`--name` changes the display name too; with the same slug, only the name):

```sh
tm thread stop t-0012 --project termilator        # every running thread first; tm says which
tm project rename termilator terminatr --name Terminatr
tm thread restart t-0012 --project terminatr      # back in its moved worktree, its conversation resumed
```

It moves `projects/<slug>/` and `worktrees/<slug>/`, reconnects git with each moved worktree (`git worktree repair`), renames `[projects.<slug>]` in `config.toml` (comments and order stay), rewrites the worktree paths in the thread records, carries the ticker's memos over and Claude's conversations and folder settings for the moved folders, and journals `project.rename` in the project. The journal, inbox, tasks and memory move with the folder unchanged. It is refused while a thread of the project runs (stop them; the error names them), and when the new slug is taken by a project, a `worktrees/<new-slug>/` folder or a `[projects.<new-slug>]` table. A running coordinator is stopped and started again under the new slug. Branches keep their names: `tm/<old-slug>/…` stays on existing threads, and only new threads get `tm/<new-slug>/…`. With no server running, the CLI does the same itself.

### Moving a repository

When a project's repository moves (renamed folder, new disk), tell the project: `tm project repo add NEW --project <slug>` and `tm project repo remove OLD --project <slug>`. A thread whose recorded repo is gone then follows its branch to the repo that has it on `tm thread restart` and `tm thread resolve` (its worktree reconnected with `git worktree repair`, its record updated). When no repo of the project has the branch, restart refuses with `no-repo`, and resolve says the repo is gone and keeps the worktree, without running git.

The build directory moves with a checkout: libghostty-vt's pkg-config files name their prefix relative to themselves (`${pcfiledir}`), which `make` rewrites once after building it, so a `.build/` built before that still links the library at the path it was built at until the next `make` there. A thread's worktree has no `.build/` of its own; `make` and `make env` in it use the main checkout's.

## The server

Any `tm` command starts the server when it isn't running, so normally you don't start it yourself. It runs detached from your terminal: closing the window, the client or an SSH session never stops it or its agents.

```sh
tm server status          # pid, uptime, version, sessions; what the last restart resumed or lost
tm server stop            # asks first while agents run; --yes to skip
tm server restart         # stop, start, resume the agents
tm server stop --force    # a hung server: SIGKILL the pid that holds the lock
```

### Over SSH (macOS)

On macOS a server started from an SSH login would run in that login's security session, and so would every session under it: the keychain refuses them, so the PR CLI (`gh`, or `az` if its token cache is in the keychain) says its token is invalid and git's credential helper fails ("Interaction with the Security Server is not allowed"). So every start (`tm server start` or `restart`, `tm update`'s restart, a command that auto-starts it, the TUI) goes through launchd: tm hands the server to your desktop session (`launchctl kickstart gui/<uid>/dev.terminatr.server`), wherever you typed the command. Starting over SSH is then the same as starting at the Mac, with the same environment: the server gets your shell's (`PATH`, `LANG`, tokens), less the SSH login's own variables.

That needs someone logged in at the Mac's console; the screen can stay locked. With nobody logged in, a start refuses:

```
tm server start: nobody is logged in at the Mac's console, so the server can't run in the desktop's session and its sessions couldn't use the keychain (the PR CLI's login, git credential helpers); log in on the Mac (the screen can stay locked) and try again, or start a server without the keychain: tm server start --no-launchd
```

`tm server start --no-launchd` (or `TERMINATR_LAUNCHD=off`) starts the server from your session as before, and over SSH warns that its sessions can't use the keychain. If launchd fails for another reason, the error names `launchctl` and the same way out.

Azure DevOps over SSH is not verified yet: whether `az`'s token cache works from a server started over SSH (it lives in `~/.azure/msal_token_cache.*`, in the keychain only when the Azure CLI's encrypted-cache setting is on) hasn't been run on a Mac. To check: log in at the Mac (`az login`), then from an SSH login run `tm server restart`, and from a session in it `az account get-access-token --resource 499b84ac-1321-427f-aa17-267ca6975798 -o none` and `tm doctor` (its `az login` and `az repo` lines). Note the result here.

`tm doctor` runs the code-host checks (`gh auth`, the `az` lines, `git origin`) in the server's context, not this shell's, so over SSH it doesn't warn "not logged in" for a `gh` that works in the sessions: lines are marked `[server]`, and a `local shell` line notes what this shell can't do (for instance reach the keychain) that the server's sessions can. With no server the checks run in this shell and are marked `[local]`. In `--json` each check has an added `source` field. `tm doctor` also shows whether the running server's sessions can reach the keychain (`server keychain`), wherever you run it from. The fix is `tm server restart`; `tm doctor --fix` offers it over SSH too, unless you run doctor from inside one of tm's own sessions. On Linux none of this applies.

What launchd holds: the job `dev.terminatr.server` (another `TERMINATR_HOME` gets `dev.terminatr.server.<hash>`), from `~/Library/LaunchAgents/` when the login service is installed, else from `run/dev.terminatr.server.plist`, which isn't loaded at login; `run/launch.json` (private) holds the environment; the server's own output goes to `logs/service.log`. `launchctl print gui/$(id -u)/dev.terminatr.server` shows it. `tm server stop` boots out a non-default home's on-demand job (nothing stays loaded for a dev or test home), and `tm doctor` lists `dev.terminatr.server.<hash>` jobs that are idle and whose plist is gone, in a temporary directory, or names a `tm` that no longer exists; `tm doctor --fix` boots them out.

`tm server stop` and `tm server restart` work whatever version the running server is. When a newer `tm` meets an older server, other commands say `the running tm server is older than this tm …; run 'tm server restart'`, and that is the fix: restart stops the old server (asking it in its own protocol, or with `SIGTERM` when it can't be asked) and starts this `tm`'s, which resumes the agents. tm only ever signals the process that holds this home's server lock and runs `tm server run`.

A `tm` from before this fix can't do that: it prints `tm server speaks protocol 1 …, this tm speaks 4 …; run 'tm server restart'` and the restart fails the same way. Get out once with (on Linux the pid file is in `$XDG_RUNTIME_DIR/terminatr/` when that is set):

```sh
kill $(cat ~/.terminatr/run/server.pid) && tm server start
```

The old server shuts down cleanly on `SIGTERM`, so the new one resumes the agents as after any restart.

### Crashes and restarts

When the server stops, every session it hosts stops with it. The next server reads `state/sessions.json` and:
- resumes every coordinator and thread whose agent has worked on a prompt, with the agent's latest session id (`claude --resume <id>`), in the same directory with the same brief and hooks;
- starts an agent that never got a prompt fresh (there is no conversation to resume);
- does not restore shell sessions; they are listed as lost.

Turns that were running are lost; resumed agents are idle. Each affected project gets a `server-restart` inbox item ("the server restarted after a crash: 2 session(s) resumed, 1 not restored"), and the coordinator decides what to re-prompt. `tm server status` lists the resumed and lost session ids, and `server.log` names each one (`server restarted after crash; resumed coordinator, t-0003; lost shell s-12`).

### Start at login (optional)

Any `tm` command starts the server when needed, so you don't need a service. If you want the server up from login:
- `tm server service install` writes `~/Library/LaunchAgents/dev.terminatr.server.plist` and loads it with launchd on macOS (`RunAtLoad`, no `KeepAlive`). It is the job every start uses anyway ([Over SSH](#over-ssh-macos)); installing it while the server runs restarts the server, and agents are resumed. On Linux it writes `~/.config/systemd/user/terminatr.service` and enables it with `systemctl --user enable --now`.
- The service runs `tm server run` (`--launchd` on macOS) with the `PATH` of the shell you installed it from, so `claude` and `git` are found. On macOS each start from a shell also hands the server that shell's environment, and a start after `brew upgrade` rewrites the plist for the new `tm`; on Linux re-run install after moving `tm` or changing `PATH`.
- On macOS the service's own output goes to `~/.terminatr/logs/service.log`; the server still logs to `server.log`.
- `tm server service uninstall` unloads and removes the file. That cleanly stops a server the service started, so the next server resumes its agents.
- `--print` shows the file without installing anything.
- On Linux a user service stops at logout unless lingering is on (`loginctl enable-linger`).

## Code hosts: GitHub and Azure DevOps

Each repo's pull requests are asked of its code host, picked from `git remote get-url origin` (SPEC §3, **Code host**). `github.com` and anything unrecognised is GitHub; these remotes are Azure DevOps (cloud, `dev.azure.com`):

```
https://[user@]dev.azure.com/{org}/{project}/_git/{repo}
https://{org}.visualstudio.com/[DefaultCollection/]{project}/_git/{repo}
git@ssh.dev.azure.com:v3/{org}/{project}/{repo}
{org}@vs-ssh.visualstudio.com:v3/{org}/{project}/{repo}
```

For an Azure DevOps Server (or any host terminatr doesn't recognise) set it in the project's `PROJECT.md` front matter:

```toml
code_host = "azure"                         # or "github"
azure_url = "https://tfs.example.com/tfs/Coll"   # organization or collection URL
```

**GitHub:** `gh auth login`.

**Azure DevOps:** the server runs on your machine and uses your login, as `gh` does.

1. Install the Azure CLI (`brew install azure-cli`). The `azure-devops` extension (`az extension add --name azure-devops`) is optional: threads may open PRs with `az repos pr create`, else they use `az rest`.
2. `az login` (Microsoft Entra ID), in the tenant the organization is connected to (`az login --tenant <id>` when you have several; `tm doctor` names it). That is all terminatr needs; it never stores or asks for a token. terminatr asks Azure DevOps' REST API with `az rest --resource 499b84ac-1321-427f-aa17-267ca6975798`, the Entra token of your login, as Microsoft documents for ad hoc REST calls ([Issue Entra tokens with Azure CLI](https://learn.microsoft.com/azure/devops/cli/entra-tokens)); Conditional Access applies to it as to any sign-in of yours. A personal access token also works by exporting `AZURE_DEVOPS_EXT_PAT` before `tm server restart`: tm uses it when `az` is missing, isn't logged in or is refused, and sends it only to the organization's URL, never on a command line; the guard stops threads reading or printing it. A PAT saved with `az devops login` is not used.
3. `git` needs its own credential for the remote (Git Credential Manager, or an SSH key), since threads push with it.
4. Run `tm doctor`: it checks `az` and the login (or the PAT), that tm can read each repo (and if not, why: the login, the organization's tenant, the origin URL), and that `git ls-remote origin HEAD` doesn't prompt.

If your email is both a work account and a personal Microsoft account, the `azure-devops` extension's commands (`az repos`, `az devops`, `az pipelines`) sign in as the personal one and fail with `TF400813: The user '…' is not authorized`, while `az rest` works. tm doesn't use them; `tm doctor` says so when it finds it. For `az repos` yourself, `az devops login --organization <org URL>` with a PAT works around it.

Threads open their pull requests themselves (`gh pr create`, `az repos pr create` or an `az rest` POST) and report the URL on the `PR:` line, either form. Completing a PR is the coordinator's with `merge = "coordinator"`, and the guard refuses a thread's `gh pr merge` or `az repos pr update --status completed`. Build-validation policies are optional: a PR without them has no checks to follow, and merge commits and squash merges are both recognised from the PR's state.

Without the CLI and a login (or a PAT for Azure DevOps), that host's PRs are not followed: no follow-up prompts, no auto-close on merge, no completing tasks on merge, and after 3 failed polls the project's inbox gets a "PR host failing" item. Threads, tasks and reports are unaffected. The full table is in SPEC §3. terminatr's own release flow (below) is GitHub-only and unrelated to the hosts of your projects.

## Checking an installation: tm doctor

`tm doctor` checks your installation and changes nothing:
- the `tm` build and libghostty-vt, git, and for each code host your projects' repos use: `gh` and its login for GitHub; for Azure DevOps `az` and `az login` (or `AZURE_DEVOPS_EXT_PAT` set, never shown), whether the optional `azure-devops` extension is installed (and, when it signs you in as a personal Microsoft account the organization doesn't have, that `TF400813`), that tm can read each repo through `az rest` (else why: the login, the organization's tenant with the `az login --tenant` to run, the origin URL), and that `git ls-remote origin HEAD` works without a password prompt. With no project yet, the GitHub checks run; and whenever `az` is on the server's `PATH` its `az` and `az login` lines show even when no repo uses Azure DevOps, marked "no project uses Azure DevOps" and never a warning (the per-repo lines stay with Azure repos);
- how `tm` was installed (Homebrew, a direct download, or built from source) and whether a newer release exists, with the command that updates it;
- the server: running and answering, the same build as this `tm` (a server of an older protocol is a warning; `tm doctor --fix` restarts it, agents are resumed), on macOS whether its sessions can reach the keychain (not when it was started over SSH without launchd; see [Over SSH](#over-ssh-macos)), a previous crash, stale `tm.sock`, `server.pid` and session runtime dirs (it never starts a server);
- each agent's installed version against its manifest's `tested_versions`. An untested Claude still works, but terminatr stops trusting its undocumented status file and messaging socket;
- the sandbox tools Claude needs for threads: `sandbox-exec` on macOS, `bwrap` and `socat` on Linux;
- Claude plugins you have enabled that are known to be unsafe in terminatr's sessions (from `claude plugin list --json`), each with the reason and the `claude plugin disable` command; today `worktrees@supermods`, whose "Remove N finished" removes a fresh thread's clean worktree. A warning only: doctor never disables a plugin;
- a session whose queued prompts are held while its agent is idle (`prompt queue`: text left in its prompt box, or a dialog), which also holds a coordinator's nudges;
- leftovers: worktrees under `~/.terminatr/worktrees` whose thread is resolved or gone, and `tm/<project>/…` branches already merged into the default branch;
- upkeep: a project's `CONTEXT.md`, `MEMORY.md` or memory file over its size budget (the coordinator consolidates it);
- settings in `config.toml`: keys tm doesn't know under `[keys]`, `[ui]` or `[mods]` (ignored), and a project's (or All projects') Complete tasks still set to the removed "when released" (it now means by you; pick again in Settings).

It exits 1 only when a check fails; warnings don't count. `--json` prints the results for scripts.

`tm doctor --fix` lists what it would remove or restart and asks before doing anything. `--yes` skips the question, and you need it when you're not on a terminal. Worktrees with uncommitted changes and unmerged branches are always kept. A branch whose PR was squash-merged looks unmerged to git; delete it yourself with `git branch -D`.

## Logs

- `~/.terminatr/logs/server.log`: the server's log: starts and stops, sessions, agent state changes, resumes. Start here when something looks wrong.
- `tm agent explain <session>`: why an agent session is in its current state (which signal decided it).
- A server started in the foreground (`tm server run`) logs to stderr instead.

## Upgrading

```sh
tm update            # asks, then installs the latest release
tm update --check    # only says whether there is one
```

- **Direct install:** `tm update` downloads the archive for your platform, checks it against `checksums.txt` and, on macOS, checks its Developer ID signature with `codesign`, then replaces `tm` in one rename. It needs to write to the directory `tm` is in. `--yes` skips the question (and is needed without a terminal).
- **Homebrew:** `tm update` never touches Homebrew's files: it shows `brew upgrade terminatr` and runs it if you say yes.
- **Built from source:** `tm update` refuses; `git pull && make`.

The running server keeps the old build until it restarts, and keeps working meanwhile: it runs from its own copy of its binary (`~/.terminatr/run/bin/tm`), so attaching and agent hooks are unaffected. Restarting switches it to the new build but stops every session: agents are resumed and lose only the turn they are in, shells are lost. So `tm update` asks before restarting (or restarts with `--restart`), and otherwise leaves it to you:

```sh
tm server restart
```

On a terminal the restart asks again before stopping agents that are mid-turn (`--yes` skips that). The restart works even when the server is older than the `tm` you ran `tm update` with.

On macOS the restart goes through launchd, so upgrading and restarting over SSH is fine as long as someone is logged in at the Mac ([Over SSH](#over-ssh-macos)).

### macOS privacy prompts

The server runs from `~/.terminatr/run/bin/tm` whatever version is installed. macOS counts every agent, `git` and tool a session runs as the server's doing (it is their *responsible process*), so their prompts, like *"tm" would like to access files on a network volume* or *"tm" would like to access data from other apps*, name `tm`, and the answer is kept for that path. After 0.11.3 the path stays the same across upgrades, so you answer once. Only the server's binary changes at that path, and macOS accepts the new one because the Developer ID signature has the same identifier (`dev.terminatr.tm`) and team.

Versions up to 0.11.3 ran from a path that changed every release, so System Settings › Privacy & Security › Files & Folders may list one `tm-v0.x.y+…` entry per release and one `tm` per Homebrew version (`/opt/homebrew/Cellar/terminatr/<version>/bin/tm`). macOS never matches them again, so they do no harm. To clear them:

- **System Settings:** in Privacy & Security › Files & Folders (and Full Disk Access, if you added `tm` there), select an old entry and click **−** (remove), or turn its switches off. Not every list on every macOS version has the **−** button.
- **tccutil:** `tccutil reset <service>` resets that permission for every app, not just `tm`, and they all ask again. Use `SystemPolicyNetworkVolumes` (network volumes), `SystemPolicyAppData` (data from other apps), or `SystemPolicyDocumentsFolder`, `SystemPolicyDownloadsFolder` and `SystemPolicyDesktopFolder`. `tccutil reset All dev.terminatr.tm` doesn't reach these entries, because macOS stores command-line tools by path, not by identifier.

Then restart the server (`tm server restart`) and answer the next prompt for the `tm` at `~/.terminatr/run/bin/tm`. If you'd rather not be asked, you can add that file to Full Disk Access, but that gives every agent you run full disk access.

To see what asked, stream the privacy log while you reproduce the prompt. Each request names the service and the process it holds responsible:

```sh
/usr/bin/log stream --style compact --predicate 'subsystem == "com.apple.TCC" AND eventMessage CONTAINS "AUTHREQ"'
```

### Upgrading from Termilator

Terminatr was called Termilator up to v0.6.2 (`brew install termilator`, state in `~/.termilator`). Nothing moves such an install over; by hand, with the old `tm`:

1. `tm server service uninstall` if you installed the login service, then `tm server stop --yes`.
2. Homebrew: `brew uninstall termilator`, `brew untap theclifmeister/termilator`, then tap, trust and install Terminatr as above (`brew tap theclifmeister/terminatr …`).
3. `mv ~/.termilator ~/.terminatr`, and edit the paths that still name `~/.termilator` by hand: in `~/.terminatr/config.toml` and in the files under `~/.terminatr/projects/`.
4. `git worktree repair` in each thread worktree (under `~/.terminatr/worktrees/`), and rename `TERMILATOR_*` variables to `TERMINATR_*`.
5. `tm server start` (and `tm server service install` again if you use it).

### Upgrading from Termalator

Termilator was called Termalator up to v0.1.0 (`brew install termalator`, state in `~/.termalator`). Termilator v0.2.0 to v0.5.2 moved such an install over by themselves; later releases don't. To upgrade from Termalator by hand: stop its server (`tm server stop` with the old `tm`), `brew uninstall termalator`, install Terminatr as above, `mv ~/.termalator ~/.terminatr` (editing the paths in it as for Termilator), run `git worktree repair` in each thread worktree, and rename `TERMALATOR_*` variables to `TERMINATR_*`. Or install the v0.5.2 release archive first, run `tm server restart` with it, then upgrade from Termilator as above.

## Keeping agent sessions lean

A fresh Claude Code coordinator or thread starts at ~45-47K tokens of context (measured from transcripts, Claude Code 2.1.292, 2026-10-07, T109). terminatr's own share is ~5K for a coordinator (its rules and `tm context`, in one `terminatr` context block) and ~2K for a thread (brief, rules, task state). The rest is Claude Code's, and comes with every Claude session: terminatr doesn't filter it, since agents keep the environment of any Claude session. You can trim it in your Claude Code settings. Rough token costs (bytes ÷ 3.8, from the request snapshot):

| What | ~tokens | Setting |
|---|---|---|
| Artifact tool schema | 14.5K | `"permissions": {"deny": ["Artifact"]}` (a bare tool name removes the tool from the context) |
| SendFeedback, Workflow, ScheduleWakeup tools | 2.4K + 2.4K + 2.1K | same, `"SendFeedback"`, `"Workflow"`, `"ScheduleWakeup"` |
| ReadNotifications, ReportFindings, ListAgents tools | 1.1K + 0.8K + 0.5K | same |
| Skills listing (built-in, claude.ai-synced, yours) | 6.3K (synced ~3.2K, built-in ~2.2K, `~/.claude/skills` ~1K) | `"skillOverrides": {"<skill>": "off"}` (or `"name-only"`), `"disableBundledSkills": true`, `"syncClaudeAiSkills": false` (user settings only) |
| claude.ai connectors (Gmail, Calendar, Drive, Claude Docs): deferred tool names, instructions, loaded Docs tools | ~3K | `"disableClaudeAiConnectors": true`, or `ENABLE_CLAUDEAI_MCP_SERVERS=false` for one shell |
| Auto memory section of the system prompt | 0.5K | `"autoMemoryEnabled": false` (user settings only); terminatr keeps its own memory |

Keep `AskUserQuestion` (a thread's question menus reach you through it), `Agent`, `Bash`, `Read`, `Edit`, `Write`, `Skill` and `ToolSearch`. A thread's sandbox instructions (~2K) come with the sandbox terminatr turns on for threads: keep them.

Where: `~/.claude/settings.json` applies to every Claude session, yours included. For the coordinator alone, a project folder's `.claude/settings.json` (`~/.terminatr/projects/<slug>/.claude/settings.json`) works for all but the user-only keys. Turning off the Artifact tool and the connectors alone takes a fresh coordinator from ~47K to ~29K tokens. Check with `/context` in a fresh session.

## Uninstalling

```sh
tm server service uninstall   # only if you installed the service
tm server stop --yes
rm "$(command -v tm)"
rm -rf ~/.terminatr          # all projects, tasks, reports and thread worktrees: keep a copy if you want them
```

Thread branches (`tm/<project>/…`) live in your repositories and are not removed by this; `tm doctor --fix` before uninstalling deletes the merged ones.

## Releasing

Push a `v*` tag (`git tag v0.2.0 && git push origin v0.2.0`). `.github/workflows/release.yml` then, on one macOS runner:

1. imports the Developer ID certificate into a temporary keychain;
2. builds and checks a snapshot (`make release-snapshot`, `scripts/release/check.sh`), unsigned;
3. runs goreleaser (`make release`): every target cross-compiled with `zig cc`, each darwin binary signed (hardened runtime) and notarised by `scripts/release/sign.sh`, archives and `checksums.txt` uploaded to a draft release;
4. checks the archives again with `--signed` (Developer ID, hardened runtime, Gatekeeper says `Notarized Developer ID`), and only then publishes the release;
5. rewrites `Formula/terminatr.rb` from `checksums.txt` (`scripts/release/formula.sh`) and merges it through a pull request. Prereleases (`v1.2.0-rc1`) skip this step.

Before the tag, `ci.yml` on main has run `test` (unit tests with `-race`) and `macos` (build, unit tests, e2e smoke): wait for them to be green. Pull requests run neither macOS nor `-race`, and the release snapshot only when they touch release files; the tag run is where every release build is checked. If the repository requires status checks, require `test` (the one check every pull request runs). `macos` is main-only, so don't require it; `changes`, `claude-mod` and `release-snapshot` are skipped when not needed, which counts as passed.

`make release-snapshot` runs the same build locally without a tag, signing nothing: `sign: TM_SIGN_IDENTITY not set; … stays ad-hoc signed` in the log. To try signing locally, set `TM_SIGN_IDENTITY` to a Developer ID identity in your keychain, by its SHA-1 hash from `security find-identity -v -p codesigning` (codesign refuses a name that two certificates share, as after a renewal); add `TM_NOTARY_KEY` (path to the .p8), `TM_NOTARY_KEY_ID` and `TM_NOTARY_ISSUER` to notarise.

### Secrets

The same Apple credentials as toe. Put them in the `release` environment (Settings → Environments → release), not as repository secrets, and limit that environment to `v*` tags (Deployment branches and tags → Selected → tag pattern `v*`): then only the release job can read them.

| Secret | What |
|---|---|
| `SIGNING_CERT_P12_BASE64` | the "Developer ID Application" certificate and its private key as .p12, base64; include the Developer ID Certification Authority intermediate |
| `SIGNING_CERT_PASSWORD` | the .p12's password |
| `KEYCHAIN_PASSWORD` | any random string, for the job's temporary keychain |
| `APPLE_TEAM_ID` | the team id in the certificate's name, e.g. `ABCDE12345` |
| `NOTARY_KEY_P8_BASE64` | an App Store Connect API key (.p8, role Developer), base64 |
| `NOTARY_KEY_ID` | that key's id |
| `NOTARY_ISSUER_ID` | the App Store Connect issuer id |

`GITHUB_TOKEN` is provided by Actions.

### Repository settings

- **Settings → Actions → General → Workflow permissions:** allow GitHub Actions to create and approve pull requests, or the formula pull request can't be opened.
- **Rules on `main`:** the formula pull request carries `[skip ci]` and merges at once, as toe's cask bump does. A ruleset that requires pull requests is fine; one that also requires status checks or approvals blocks it. Then the job turns on auto-merge (if Settings → General allows auto-merge) and warns; merge it by hand, or add GitHub Actions as a bypass actor of that rule.
- **Forks:** no workflow uses `pull_request_target` or `workflow_run`, and the only one that sees secrets runs on tags, which only people with write access can push. For a public repository keep Settings → Actions → "Fork pull request workflows" at requiring approval for outside contributors.
- **Self-hosted runners:** none. On a public repository any fork pull request can target a self-hosted runner's labels from a workflow of its own and run code on that machine, so don't register one for this repository (the real-Claude suite runs by hand, `make test-claude`).

