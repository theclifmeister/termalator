# Spike: shared state via symlinks (t-0005)

Can a Termalator thread (Claude Code in a git worktree) read shared project state through
symlinks, and write its own report somewhere the coordinator sees it, without prompts and
without the files leaking into commits?

**Short answer:** symlinks are not a workaround for Claude Code's permissions. Claude resolves
every link and checks the **real path**, so a link that leads out of the worktree counts as
"outside the working directory". It prompts for reads in every non-bypass mode and refuses
writes. Grant access explicitly instead. Read-only `Read(...)` allow rules give live reads with
no prompt, and a narrow `--add-dir` covers the thread's output folder. With those grants the
worktree no longer needs a `.termalator/` folder, which also removes the git-hygiene and
data-loss problems (see [Recommended layout](#recommended-layout)).

## Versions and setup

| | |
|---|---|
| Claude Code | 2.1.289 (`/Users/clifford/.local/bin/claude`), model `haiku` for probes |
| OS | macOS 27.0.1 (26A434), arm64 |
| git | 2.54.0 (Apple Git-157) |
| Codex CLI | 0.160.0 installed, **not tested** (docs/help only) |
| pi | not installed, **not tested** |

Scripts (throwaway quality):

- `setup.sh` builds `~/.termalator-spike/` from scratch:
  - a project folder `projects/demo/` (CONTEXT, TASKS, MEMORY, memory/, threads/t1/brief.md, each holding a unique token)
  - a repo `repos/demo` with a worktree `worktrees/demo-t1`
  - the §4.3 layout: `.termalator/project -> projects/demo`, `.termalator/brief.md -> …/threads/t1/brief.md`, a real `.termalator/out/`, and `projects/demo/threads/t1/out -> <worktree>/.termalator/out`
  - `.termalator/` added to `info/exclude`
- `probe.sh` runs one `claude -p` from the worktree with:
  - `--setting-sources project,local`, so the user's own hooks and `defaultMode: auto` don't interfere
  - `--permission-prompts none`, so anything that would prompt is denied and listed in `permission_denials`
  - `--output-format json`
- `run-matrix.sh <config>` runs 10 probes per permission configuration and records what actually landed on disk (`results/<config>/files.txt`).
- `run-coordinator.sh`, `git-hygiene.sh`, `alternatives.sh`, `imports.sh`, `socket.sh` cover the other questions.
- `verify-recommended.sh` checks the recommended layout. `summarize.py` produces `results/summary.txt` (all raw JSON is in `results/`).

"Prompt" below means Claude listed a permission denial with "requires approval" / "you haven't
granted it yet". In a live TUI that is a permission dialog. I did not drive an interactive
session to watch the dialogs themselves.

## 1 + 2. Reads and writes through symlinks (Claude Code)

Probes, run from the worktree:

| id | action |
|---|---|
| R1 | Read tool `.termalator/project/CONTEXT.md` (file inside a dir symlink) |
| R2 | Read tool `.termalator/brief.md` (file symlink) |
| R3 | Bash `cat .termalator/project/memory/prefs.md` |
| R4 | Read tool, absolute real path in the project folder (control) |
| W1 | Write tool `.termalator/out/REPORT.md` (real dir inside worktree, linked back from project) |
| W2 | Write tool, new file `.termalator/project/w2.md` (through dir symlink, lands outside) |
| W3 | Bash `echo … > .termalator/out/STATUS.md` |
| W4 | Bash `echo … > .termalator/project/w4.md` (through dir symlink) |
| W5 | Write tool, absolute path in the project folder (control) |
| E1 | Read + Edit tool on `.termalator/brief.md` (file symlink, edit in place) |

Results (✅ done silently, 🟡 would prompt, ⛔ hard-blocked):

| config | R1 | R2 | R3 | R4 | W1 | W2 | W3 | W4 | W5 | E1 |
|---|---|---|---|---|---|---|---|---|---|---|
| `manual` (default) | 🟡 | 🟡 | 🟡 | 🟡 | 🟡 | 🟡 | 🟡 | 🟡 | 🟡 | 🟡 read |
| `acceptEdits` | 🟡 | 🟡 | 🟡 | 🟡 | ✅ | 🟡 | ✅ | 🟡 | 🟡 | 🟡 read |
| `acceptEdits` + sandbox¹ | 🟡 | 🟡 | ✅ | 🟡 | ✅ | 🟡 | ✅ | ⛔ OS | 🟡 | 🟡 read |
| `auto` (in `-p`)² | 🟡 | 🟡 | 🟡 | 🟡 | 🟡 | 🟡 | 🟡 | 🟡 | 🟡 | 🟡 read |
| `acceptEdits --add-dir <project>` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ⛔ link |
| same via `--settings` `additionalDirectories` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ⛔ link |
| `--add-dir` + sandbox | ✅³ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ⛔ link |
| `acceptEdits` + `Read(/<project>/**)` | 🟡 | 🟡 | ✅ | ✅ | ✅ | 🟡 | ✅ | 🟡 | 🟡 | 🟡 read |
| **`acceptEdits` + `Read(/<project>/**)` + `Read(/<wt>/.termalator/**)`** | ✅ | ✅ | ✅ | ✅ | ✅ | 🟡 | ✅ | 🟡 | 🟡 | ⛔ link |
| `--dangerously-skip-permissions` | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ⛔ link |
| bypass + sandbox | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ⛔ OS | ✅ | ⛔ link |

¹ `--settings '{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true}}'`. The macOS sandbox
(Seatbelt) is available and works on this Mac.
² Auto mode in `-p` with `--permission-prompts none` denied even W1, a plain write inside the
worktree. So this row says nothing about auto mode in a live TUI. **Untested interactively.**
³ First run: the model typed a wrong path. Re-running under `--add-dir --settings additionalDirectories` succeeded.

What this shows:

1. **Claude Code resolves symlinks before every permission check.**
   - The denial text says so: "`.termalator/brief.md` resolves through a symlink to `…/projects/demo/threads/t1/brief.md`, which is outside the allowed working directories."
   - A symlink therefore buys nothing over an absolute path (R1/R2 behave like R4).
   - **Reading outside the working directories prompts**, even in `acceptEdits` mode.
2. **A Read allow rule must match *both* the link path and its target.**
   - With only `Read(/<project>/**)`, the absolute path (R4) and Bash `cat` (R3) work, but the Read tool through the link (R1/R2) still prompts.
   - Adding `Read(/<worktree>/.termalator/**)` fixes it.
   - The rule syntax for an absolute path is `Read(//abs/path/**)`. That is `Read(/` followed by an absolute path, as in `run-matrix.sh`.
3. **Read-only rules keep writes gated.** W2/W4/W5 still prompt under the read-only config. This is the "agents never write project files directly" property §4.3 wants.
4. **The Write/Edit tools never write to a file that is itself a symlink**, even in bypass mode: "Refusing to write …/brief.md: it is a symbolic link. Write to the link's target path instead." Creating a *new* file inside a symlinked *directory* works once the target dir is allowed (W2 with `--add-dir`).
5. **Writing the thread's report into a real dir inside the worktree just works** (W1, W3) under `acceptEdits` or the sandbox, and the coordinator sees it through `threads/t1/out`.
6. **The Bash sandbox checks real paths at the OS level.**
   - A write through the link to the outside (W4) fails with `operation not permitted`, with no prompt.
   - Sandboxed reads are unrestricted: R3 `cat` worked where the Read tool prompted. So with the sandbox on, the two tools disagree.
   - `--add-dir` directories are writable inside the sandbox.
7. **The coordinator has the mirror-image problem** (`run-coordinator.sh`):
   - Running in the project folder, reading `threads/t1/out/REPORT.md` prompts in `manual` and `acceptEdits`, because the real file is in the worktree.
   - Granting the worktrees root fixes it (`coord-adddir`).
8. **Sandboxed Bash cannot reach a unix socket by default** (`socket.sh`):
   - `connect()` to `~/.termalator-spike/tm.sock` fails with `PermissionError: [Errno 1] Operation not permitted`.
   - With `"sandbox":{"network":{"allowUnixSockets":["<sock>"]}}` it works.
   - Relevant because `tm report`/`tm task` would talk to the daemon from inside the agent's Bash.
9. **`--add-dir` is variadic.** `claude --add-dir DIR "prompt"` takes the prompt as a second directory and fails with "Input must be provided…". Pass the prompt on stdin, put `--add-dir` before another flag, or use `--settings '{"permissions":{"additionalDirectories":[…]}}'`, which behaves the same.

## 3. Git hygiene (`git-hygiene.sh`, `results/git-hygiene.txt`)

- **Exclude file.** A linked worktree has no exclude file of its own: `git rev-parse --git-path info/exclude` returns the *main repo's* `.git/info/exclude`. One `.termalator/` line there covers the main checkout and every worktree, which is fine because they all use the same name. A global `core.excludesFile` also works, but it edits the user's git config for every repo; avoid it.
- **With the exclude**, `git status` is clean and `git add -A` picks up nothing. Without it, `git add -A` would stage `.termalator/project` as a symlink blob, plus `brief.md` and `out/REPORT.md`.
- **`git clean -fd`** leaves `.termalator/` alone. **`git clean -fdx` deletes it**, report included. Agents do run `clean -fdx`.
- **`git worktree remove` (no `--force`) succeeds and silently deletes ignored files, including `.termalator/out/`.**
  - Afterwards `projects/<slug>/threads/<id>/out` is a dangling link and **the report is gone**.
  - The shared files are untouched: git removes the symlink, not its target.
  - So with the §4.3 layout, any removal not done through `tm` (the user, herdr, `git worktree prune` after an `rm -rf`) loses the report. `tm` would have to copy `out/` home before every removal, which brings back the "copy home" step the design wanted to drop.

## 4. Alternatives compared

| option | live? | Claude permissions | other harnesses | git / data-loss | verdict |
|---|---|---|---|---|---|
| **Dir/file symlinks into the worktree** (§4.3) | yes | still prompts. Needs `Read` rules for link and target (or `--add-dir`). Edit of a symlinked file is refused | Codex/landlock/seatbelt also resolve real paths (expected) | needs exclude. Report lost on `worktree remove` / `clean -fdx` | works, but adds nothing over absolute paths |
| **Hard links** | until the first rewrite | no prompt (a normal in-cwd file) | fine | needs exclude | ✗ Claude's Edit tool replaced the inode (the project copy stayed unchanged). tsk-style atomic rename in the project broke the link too. No directories. Same volume only |
| **Copy + sync** | no (snapshot) | no prompt | fine | needs exclude. Two-way conflicts | ✗ This is herdr-projects' snapshot problem again |
| **CLAUDE.md / CLAUDE.local.md `@import` of absolute paths** | no: loaded at session start | in `-p`, **external imports (absolute, or via a link that leaves the cwd) were silently dropped**, even with `--add-dir` and bypass. In-worktree imports loaded. Interactive mode presumably asks once (untested) | Claude only | the CLAUDE file itself must be excluded | ✗ for live context. Use `--append-system-prompt-file` / `SessionStart` for the brief, as §2 already plans |
| **`--add-dir` / `additionalDirectories`** | yes | no prompt, read **and write** to the whole dir. Edits auto-accepted in `acceptEdits` | Codex `--add-dir` (writable roots). pi: n/a | nothing in the worktree | ✓ for a small dir the thread should write (its own out/). Too broad for the whole project folder |
| **`Read(//abs/**)` allow rules** via `--settings` | yes | reads silent, writes still gated | Codex workspace-write reads everywhere by default. pi: no gate | nothing in the worktree | ✓ **best fit for shared context** |

## 5. Codex and pi (from docs/help only, not tested)

**Codex** (`codex-cli 0.160.0` help):

- `--sandbox read-only|workspace-write|danger-full-access` and `--add-dir <DIR>`: "Additional directories that should be writable alongside the primary workspace".
- In `workspace-write`, reads are allowed everywhere. Writes are limited to the cwd, `--add-dir` roots and tmp, enforced by Seatbelt (macOS) or Landlock (Linux) on real paths. So expect:
  - shared context reads: fine with or without links
  - writes through a link that leaves the worktree: ⛔, like Claude's sandboxed W4
  - `--add-dir <project>/threads/<id>/out`: the same grant as Claude's
- Two things to test before Codex threads ship:
  1. A linked worktree's git dir is `<repo>/.git/worktrees/<id>`, outside the cwd. Codex also protects `.git` as read-only, so `git commit` from a sandboxed Codex thread may fail unless the main repo's `.git` is a writable root.
  2. `workspace-write` disables network by default, which may also block the `tm` unix socket (as with Claude's sandbox).

**pi:** no sandbox and no permission prompts by default; it reads and writes anywhere. Any layout works. Containment exists only if an extension adds it.

## Recommended layout

Change §4.3 so that **nothing Termalator-owned lives in the worktree**, and grant access
explicitly at launch:

```
~/.termalator/projects/<slug>/             # coordinator cwd
  PROJECT.md CONTEXT.md TASKS.md MEMORY.md memory/ JOURNAL.md inbox/
  threads/<id>/
    thread.toml  brief.md
    out/                                   # REAL dir, written by the thread
      REPORT.md  library/
    STATUS.md                              # written by the daemon from `tm report`
~/.termalator/worktrees/<slug>/<id>/       # git worktree: pristine, no .termalator/, no exclude needed
```

Keep worktrees out of the project folder. Claude loads `CLAUDE.md` from parent directories, so
the coordinator's role file would leak into every thread.

Claude thread launch (cwd = worktree):

```sh
claude --permission-mode acceptEdits \
  --settings '{"permissions":{
       "allow":["Read(/'"$HOME"'/.termalator/projects/<slug>/**)"],
       "additionalDirectories":["'"$HOME"'/.termalator/projects/<slug>/threads/<id>/out"]},
     "sandbox":{"network":{"allowUnixSockets":["'"$HOME"'/.termalator/tm.sock"]}}}' \
  --append-system-prompt-file ~/.termalator/projects/<slug>/threads/<id>/brief.md
```

Yolo mode (`--dangerously-skip-permissions`) needs none of this, except `allowUnixSockets` if the sandbox is on.

- **Shared context (live):**
  - The brief names absolute paths (`~/.termalator/projects/<slug>/CONTEXT.md` …), and the `Read` rule makes them silent.
  - No symlinks. A link would need a second rule, and agents would see two paths for one file.
- **Thread output:**
  - The thread writes `REPORT.md` and `library/` straight into `threads/<id>/out/`, granted by `additionalDirectories` scoped to that one folder.
  - It survives `git worktree remove`, `git clean -fdx` and a deleted worktree. The coordinator reads it in its own cwd with no rule, and the copy-home step goes away.
- **Everything else** (status, tasks, memory) goes through `tm` over the daemon socket, so agents never write those files.
- **Coordinator launch:** cwd = project folder, plus `Read(/<home>/.termalator/worktrees/<slug>/**)` so it can review thread code without prompts.
- **Codex:** `--sandbox workspace-write --add-dir <out> [--add-dir <repo>/.git]` (test the `.git` and socket points). **pi:** nothing needed.

**Verified** by `verify-recommended.sh`, run in `acceptEdits` with the sandbox off and on:

- silent: Read of `CONTEXT.md`, Bash `cat` of `memory/`, Write of `out/REPORT.md`, Bash `mkdir`/write in `out/library/`, and writes in the worktree
- prompts: overwriting `TASKS.md`, and the file stays unchanged
- the worktree's `git status` shows only the agent's own file

If the user prefers §4.3 as written (output inside the worktree, linked back):

- it works for Claude with the two `Read` rules above;
- `.termalator/` goes in `<repo>/.git/info/exclude`;
- `tm thread remove` **must copy `out/` into the project before `git worktree remove`**, and the dashboard must flag dangling `out` links.

## Not verified

- The interactive TUI's actual dialogs. The prompt/no-prompt calls rest on `-p` denial records.
- Auto mode in a live session.
- Linux (bubblewrap sandbox).
- Codex and pi behaviour.
- Whether interactive Claude offers to approve external `@imports`.

The spike files are in `~/.termalator-spike/`. Delete with `rm -rf ~/.termalator-spike`.
