# Spike t-0004: Claude Code integration — findings

> These are the findings of the `claude` spike. The spike's code (the scripts and paths this file names) was removed in T39; it is in git history under `spikes/claude/` at commit `41983d953b6d` (`git show 41983d953b6d:spikes/claude/`). The spike ran under the product's first name; product names and paths below are written as Terminatr.

Tested 2026-10-04 against **Claude Code 2.1.289** (native build, `~/.local/bin/claude`), model Haiku 4.5, on macOS 27.0.1 (arm64), Go 1.27.1. Sessions ran in a private tmux server (3.7c), standing in for Terminatr's PTY host. Every claim below comes from a scripted run in `scripts/`, unless it is marked **(untested)** or **(inferred)**. Linux was not tested.

## TL;DR

1. **Hooks are additive.** Hooks from `--plugin-dir`, `--settings` (a file or inline JSON) and a project's `.claude/settings.json` / `settings.local.json` all run *alongside* the user's `~/.claude/settings.json` hooks. Nothing gets replaced, so we never have to touch the user's config. Only `--setting-sources` removes a scope.
2. **Hook-only state goes stale, exactly as herdr found.** Three cases fire **no closing hook at all**:
   - Esc on a permission dialog: no `PermissionDenied`, no `Stop`, no `PostToolUseFailure`.
   - Esc while text is streaming: no `Stop`.
   - Esc while a tool runs: `PreToolUse` and then nothing.
3. **Claude publishes its own state**, and that fixes the stale cases. The file `~/.claude/sessions/<pid>.json` has `status` = `idle` | `busy` | `waiting`, plus `waitingFor` = `"permission prompt"` | `"input needed"`. `claude agents --json` prints the same data. It was right in every stale case. It is undocumented, so it is a strong signal but not a contract.
4. The **transcript JSONL** marks interrupts explicitly: a user entry with `[Request interrupted by user]` or `[Request interrupted by user for tool use]`. It also writes a `system/turn_duration` entry at every normal turn end. That makes it a second structured cross-check.
5. **Context re-injection works.** `SessionStart` fires with `source` = `startup` | `resume` | `clear` | `compact` | `fork`, and `hookSpecificOutput.additionalContext` reaches the model. The context can be fetched fresh from the daemon on every clear or compact. `--append-system-prompt-file` **survives `/clear`**.
6. **Use stream sockets for delivery, not datagrams.** macOS caps unix datagrams at **2048 bytes** (`net.local.dgram.maxdgram`), and larger hook payloads were silently dropped. A Go hook over a stream socket costs about 4 ms per event and exits 0 in about 3.5 ms when the daemon is down, crashed or stalled. A python3 sender costs about 18 ms.
7. **Prompt injection:** a bracketed paste followed by a separate Enter works, and prompts typed while Claude works get queued. Claude also has an undocumented **`uds-messaging` socket** (`CLAUDE_CODE_MESSAGING_SOCKET`, also in the session file) that takes `{"type":"user",...}` lines and queues them properly, both idle and mid-turn. In **2.1.291** it may require an auth line, never answers the sender, and may hold or drop a message silently, so tm uses it only for its own fixed-word prompts and as a liveness probe (§4a).
8. **Todos:** this version has **no `TodoWrite`**. The list is managed with **`TaskCreate` / `TaskUpdate`**, which send *diffs*, not the whole list. They come with `TaskCreated` / `TaskCompleted` hook events. The full list lives in `~/.claude/tasks/<session_id>/<n>.json`. Subagents don't get these tools. SPEC §8.6's `TodoWrite` mapping and §8.2's "replace the stored list" model have to change (see the todo mirroring section below).
9. Some screens appear **before any hook can run**: the workspace trust dialog and the bypass-permissions warning. Both default to **"No, exit"**. Hooks are skipped until trust is accepted, so these screens can only be seen through screen rules.

## How the spike works (reproducing)

```
spikes/claude/
  cmd/tmhook/      hook command: stdin JSON → envelope → unix socket; prints SessionStart additionalContext
  cmd/tmd/         stand-in daemon: logs events (JSONL), serves context, accepts http hooks
  plugin/terminatr/   --plugin-dir plugin subscribing to ~24 events, all calling tmhook <Event>
  scripts/lib.sh   tmux driver: clean env (env -i), trust handling, paste, screen capture, waits
  scripts/sampler.py   every 100 ms: herdr-style screen classification + ~/.claude/sessions/<pid>.json
  scripts/events.py    merged timeline: hooks + screen + session file, with a naive hook-only state
  scripts/sNN-*.sh     one scenario each (listed below)
```

Build with `go build -o bin/tmhook ./cmd/tmhook && go build -o bin/tmd ./cmd/tmd`, then run `bash scripts/s03-permission.sh` (or any other scenario). Output lands in `lab/<name>/` (git-ignored): `events.jsonl`, `screen.jsonl`, `screens.txt`, `debug.log`. Each scenario costs a few cents of Haiku usage.

| Script | Question |
|---|---|
| s01 smoke | Do plugin hooks fire, and does `additionalContext` reach the model? |
| s02 merge | Plugin / settings / inline / project / local hooks vs the user's own hooks |
| s03 permission | Permission dialog approved vs denied with Esc |
| s04 cancel, s05, s06 | Esc while streaming and during a tool; queued prompts; subagents; background tasks; AskUserQuestion |
| s07 clear-compact | `SessionStart` clear/compact, context refresh, `permission_prompt` timing, `SessionEnd` reasons |
| s08 delivery | Hook cost; daemon down, crashed or stalled; payload size limits |
| s09, s10 inject | Kickoff arg, `--append-system-prompt-file`, paste, queued paste, uds-messaging socket |
| s11–s13 | Broken hooks, statusline, resume / continue / fork / `--session-id` reuse |
| s14 | `claude agents --json` across states |
| s15 | Every stale case: hooks vs screen vs session file |
| s16–s18 | Todo / task list mirroring |
| s19 | Headless stream-json; trust in a new worktree |
| s20 | `--append-system-prompt-file` after `/clear`; read-only folder via deny rule and sandbox, interactive and yolo |

The user's own hooks were used as a probe in s02. Their orca hook POSTs to `127.0.0.1:$ORCA_AGENT_HOOK_PORT` when its env vars are set, so the spike pointed it at `tmd`. The user's config was never modified.

---

## 1. Hook injection without touching `~/.claude`

Result of s02. Every row fired once per event:

| Launch | Our hooks ran | The user's hooks ran |
|---|---|---|
| `--plugin-dir P` | yes | yes |
| `--settings file.json` | yes | yes |
| `--settings '<inline json>'` | yes | yes |
| `--plugin-dir` + `--settings` + repo `.claude/settings.json` + `.claude/settings.local.json` | all four | yes |
| same + `--setting-sources project,local` | all four | **no** (the user scope was dropped) |

- **Additive everywhere.** The settings doc's "lists merge" holds for `hooks`.
- **A project-level `.claude/settings.json` in a worktree** loads like any other (it is just the cwd's `.claude/`). We don't need it, though, and SPEC §9 rightly keeps worktrees free of anything owned by terminatr.
- **Workspace trust gates every hook**, ours included. The debug log says `Skipping SessionEnd:other hook execution - workspace trust not accepted`. The trust dialog defaults to **"No, exit"**, and keys sent within about 0.5 s of it painting are dropped. A **new git worktree of an already-trusted repo shows no trust dialog** (s19). Trust is stored per path in `~/.claude.json`.
- `--dangerously-skip-permissions` shows a **bypass warning** on first use, which also defaults to "No, exit". Setting `skipDangerousModePermissionPrompt` in `--settings` might suppress it **(untested)**.
- `--settings` also **replaces** single-valued keys such as `statusLine`. In s11 our statusline replaced the user's ccstatusline. So pass only hooks, permissions and sandbox through `--settings`, never `statusLine`.
- Claude itself warned that a `Write(path)` deny rule "is not matched by file permission checks — only Edit(path) rules are". **Use `Edit(//abs/**)` deny rules**: they cover Write, Edit and NotebookEdit. SPEC §8.6's example lists both. Drop the `Write(...)` rule.
- `TERMINATR_*` env set on the claude process **reaches hook processes** (s01: the `pane` field arrived). So does `CLAUDECODE=1`, plus `CLAUDE_CODE_MESSAGING_SOCKET` and `CLAUDE_CODE_MESSAGING_TOKEN`.

**A broken hook is visible to the user.** A missing hook binary or an `http` hook pointing at a dead port puts red `UserPromptSubmit hook error … ECONNREFUSED` / `No such file or directory` lines into the transcript UI (s11). Claude carries on, because these are non-blocking errors. So:
- The hook must be a `command` hook that always exists (the `tm` binary itself).
- It must never write to stderr, and it must always exit 0.
- Don't use `type: "http"`: it shows an error every time the daemon is down.

## 2. Which events fire when, and a reliable state

### What fired (main agent)

| Situation | Events, in order (observed) |
|---|---|
| Startup | `SessionStart(source=startup)` |
| A turn | `UserPromptSubmit` → (`PreToolUse` → [`PermissionRequest`] → `PostToolUse` → `PostToolBatch`)* → `MessageDisplay` → `Stop` |
| Permission dialog | `PreToolUse` → `PermissionRequest` immediately. **`Notification(permission_prompt)` comes 6 s later** |
| Permission approved | `PostToolUse` |
| **Permission denied (Esc)** | **nothing** |
| **Esc while streaming** | **nothing** (no `Stop`) |
| **Esc during a running tool** | **nothing** after `PreToolUse` |
| AskUserQuestion | `PreToolUse(AskUserQuestion)` + `PermissionRequest(AskUserQuestion)`; `PostToolUse` once answered |
| Prompt typed while working | queued; its `UserPromptSubmit` fires when it is dequeued, which can be **mid-turn after a `PostToolUse`** |
| Background Bash task or background agent finishes | a new turn with **`UserPromptSubmit` whose prompt starts `<task-notification>`** |
| Idle for 60 s | `Notification(idle_prompt)` exactly 60 s after `Stop` |
| `/clear` | `SessionEnd(reason=clear)` → `SessionStart(source=clear)`, **with a new session id** |
| `/compact` | `PreCompact(trigger=manual)` → `SessionStart(source=compact)`, same session id |
| `--resume` / `--continue` / `--fork-session` | `SessionStart(source=resume\|resume\|fork)` |
| `/exit` | `SessionEnd(reason=prompt_input_exit)` |
| PTY closed (tmux kill) | `SessionEnd(reason=other)` still fires |
| Task list | `TaskCreated`, `TaskCompleted` (see the todo mirroring section below) |

Never observed: `PermissionDenied` (manual mode; it probably needs auto mode's classifier **(inferred)**), `StopFailure`, `PostToolUseFailure`, `Elicitation`, `Notification(elicitation_dialog)`, `Setup`. `MessageDisplay` fires once per displayed assistant message.

### Subagents and side agents

- The **Agent tool now runs subagents in the background by default**. Even when the prompt said "foreground", `PostToolUse(Agent)` came back at once, and the main agent's `Stop` fired while the subagent kept working. The subagent's own `PreToolUse` / `PostToolUse` carry `agent_id` and `agent_type`. Its completion comes as `SubagentStop(agent_type=…)`, followed by a new main turn (`UserPromptSubmit` `<task-notification>`).
- **A `SubagentStop` with `agent_id` but no `agent_type` fires about 5–10 s after almost every `Stop`.** That is the prompt-suggestion side agent: its `last_assistant_message` is the gray suggestion shown in the input box. Ignore it.
- While a background agent runs after the main `Stop`, the session file stays `busy` and the title spinner keeps going (s15). In s06 the screen briefly showed idle in the same situation. **Treat "main turn ended but background agents are running" as working, reason `background`**, and count them with `SubagentStart` / `SubagentStop` where `agent_type` is set.

### Hooks vs screen vs session file (s15, merged timeline)

| Case | Hook-only state | Screen rules | `~/.claude/sessions/<pid>.json` |
|---|---|---|---|
| Permission dialog | blocked ✔ | blocked ✔ | `waiting:permission prompt` ✔ |
| …then Esc (deny) | **blocked forever ✘** | idle ✔ | `idle` ✔ |
| Esc while streaming | **working forever ✘** | idle ✔ | `idle` ✔ |
| Esc during a tool | **working forever ✘** | idle ✔ | `idle` ✔ |
| AskUserQuestion | blocked ✔ | blocked ✔ | `waiting:input needed` ✔ |
| Background agent after `Stop` | idle (debatable) | working (title spinner) | `busy` |
| `/clear` | ✔ with session-id rotation | brief spinner | ✔ |

The session file updated within one 100 ms sample of the screen in every case. It is written atomically by Claude, and it also carries `sessionId` (it follows `/clear`), `messagingSocketPath`, `name`, `version`, `statusUpdatedAt` and `peerFeatures`. **Terminatr knows the pid** because it spawned the process. The native `claude` binary is the PTY child (`env` exec'd it, so tmux's `pane_pid` was claude). If a wrapper is ever used, match on `sessionId`.

### Screen strings in 2.1.289 (for the rules)

- **Title** (OSC 0/2):
  - `◐`/`◑`/`◒`/`◓` + space while working (half-circle spinner; no Braille was seen in this build).
  - `✳ <session title>` while idle **and also while blocked**. So the title alone can't mean idle; a blocker rule must take priority over it.
- **Permission dialog**: `Do you want to create x.txt?` / `❯ 1. Yes` / `2. Yes, and switch to accept edits…` / `3. No`, and the footer is **`Esc to cancel · Tab to amend`, with no "Enter to confirm"**. herdr's priority-980 rule needs `enter to confirm` and its 850 rule needs the literal `do you want to proceed?`, so **neither matches this dialog**. Use `Do you want to ` + `❯ 1. Yes` + `Esc to cancel`.
- **AskUserQuestion**: `☐ <header>`, the question, numbered options plus `Type something.`, and the footer `Enter to select · ↑/↓ to navigate · Esc to cancel`.
- **Spinner line while working**: `✢ Churning… (6s · ↓ 201 tokens)`. Also `✻ Scurrying… (running Stop hooks… 0/3 · …)` and `✽ Forming… (running UserPromptSubmit hooks…)` while hooks run.
- **`esc to interrupt` was never on screen.** The user has a custom statusline (ccstatusline), which hides it, as the research notes predicted. Don't rely on it.
- **Idle**: a `❯` line inside the prompt box. Beware:
  - The prompt-suggestion ghost text renders as `❯ ! sleep 25` (dim). Only cell attributes (dim/faint), which libghostty exposes, tell it apart from typed text.
  - After an Esc cancel, Claude **puts the cancelled prompt back in the input box**. In s15 the next typed prompt got appended to it.
  - Queued prompts show `Press up to edit queued messages`.
- **Pre-hook screens**: `Yes, I trust this folder` (default `❯ No, exit`) and the bypass warning `Yes, I accept` (default `❯ No, exit`). Map both to `blocked`, reason `trust`.

### Recommended state reducer for Claude

Signals in priority order. Each one names the stale case it covers.

1. **Process exit** → `exited`. `SessionEnd` with `reason` ≠ `clear` → `exited` (`clear` is followed by a new `SessionStart`).
2. **Session file** (`~/.claude/sessions/<pid>.json`, watched with fsnotify and a 500 ms poll as backstop): `busy` → working, `waiting` → blocked (`permission prompt` → permission, `input needed` → question), `idle` → idle. **This is the primary state source.** It clears every Esc case. Because it is undocumented, guard it: if the file is missing, has no `status`, or its `version` is unknown, drop down to (3)–(5) and label the state `(hooks+screen)`.
3. **Hooks** for edges and details:
   - `UserPromptSubmit` → working.
   - `PermissionRequest` → blocked, with `tool_name` and `tool_use_id`. That is the dialog's subject, which the coordinator's `tm thread approve` needs.
   - `Stop` → idle candidate. It is overridden by background agents (`SubagentStart`/`SubagentStop` with `agent_type`) and by the session file.
   - Ignore events whose payload has `agent_id` for **state**, but keep them for the background-agent counter.
4. **Transcript tail** (`transcript_path` comes in every hook payload) as the cross-check when the session file is unavailable:
   - user text starting `[Request interrupted by user` → idle;
   - `system/turn_duration` → turn ended;
   - `queue-operation` → a queued prompt exists.
5. **Screen rules** (as in SPEC §8.4):
   - A visible dialog wins over a non-blocked state.
   - Trust and bypass screens give blocked/trust (no other source can see them).
   - Idle after an Esc is confirmed by the prompt box plus the session file or transcript, rather than by a 700 ms debounce alone.

Whether hooks lead (SPEC §8.6 open point 3): **hooks alone are not enough for Claude.** If Terminatr accepts the session-file dependency, Claude needs no screen-scraped *state* at all, only the trust/bypass screens and a blocker sanity check. If not, screen rules plus the transcript are mandatory, as in herdr.

## 3. Delivering hook events to a daemon

`cmd/tmhook` is the prototype of `tm hook --agent claude`. It reads stdin, wraps the payload in an envelope (`pane`, timestamps, `ppid`), sends it, prints `additionalContext` for `SessionStart`, and always exits 0.

Results (s08, 200–400 runs each, macOS arm64):

| Daemon | Cost per hook process | Exit | Delivered |
|---|---|---|---|
| up | ~4.1 ms (in-process work 0.1–1 ms; the rest is process spawn) | 0 | 200/200 |
| down (no socket file) | ~3.6 ms | 0 | — |
| crashed (stale socket file) | ~3.7 ms | 0 | — |
| stalled (SIGSTOP) | ~3.5 ms | 0 | 128/400: the listen backlog filled, then connects were refused immediately; no blocking |
| python3 sender (herdr style) | ~18 ms | | |

- Socket latency from hook send to daemon receive was 0.1–1.9 ms.
- **Datagrams are unusable on macOS for full payloads.** `net.local.dgram.maxdgram` = 2048 and `recvspace` = 4096, and payloads of 3 KB and up were dropped silently (1 of 5 delivered). A stream socket delivered 5 of 5, up to 100 KB. Linux allows larger datagrams, but use stream sockets on both platforms.
- Bounds per call: dial timeout 50 ms, write deadline 100 ms, `SessionStart` context request 500 ms total (3 s since T94, after a loaded machine missed it following `/clear`). The worst case is therefore about 150 ms for a normal event and about 3.1 s for `SessionStart`, and only when the daemon is wedged.
- **Trim the payload** to the fields the reducer needs: event, session, ids, `agent_id`/`agent_type`, `source`, `reason`, `notification_type`, `tool_name`, `tool_use_id`, and a 500-byte prefix of `prompt` / `last_assistant_message`. Keep **`tool_input` and `tool_response` only for `TaskCreate` and `TaskUpdate`**, which carry the todo list. A trimmed envelope is about 0.7–1 KB.
- **Hooks must be synchronous** (no `async`). At about 4 ms they are cheap. Sync keeps events in order: a pane's hooks arrive in order, and the daemon-side seq is the receive order. `SessionStart` has to be sync anyway to return context. Give them `timeout` 5 s as an outer safety net **(async mode untested)**.
- Ordering caveat: the user's own hooks run in the same hook batch. Some of the user's hooks are slow (curl with a 1.5 s timeout), and that adds to Claude's latency, but it doesn't affect ours.

## 4. Brief and prompt injection

| Mechanism | Result | Use |
|---|---|---|
| Positional kickoff prompt | ✔ even in an untrusted dir: it waits behind the trust dialog. **Gotcha:** `--allowedTools`, `--disallowedTools`, `--add-dir`, `--tools`, `--mcp-config` and `--betas` are variadic (`<x...>` in `--help`), and in s09 `--allowedTools Bash "<prompt>"` **swallowed the prompt as a tool name**. Verified with `-p`: `--allowedTools Bash Read -- "prompt"` works, but without the `--` Claude says "Input must be provided". Always pass the kickoff after `--` | kickoff |
| `--append-system-prompt-file brief.md` | ✔ The model knew the codeword. **It survives `/clear`** (s20, interactive and yolo). `--system-prompt-snapshot` (on by default) records the system prompt and reuses it until compaction, so a brief edited mid-session only takes effect after compaction or a restart | static brief |
| `SessionStart` `hookSpecificOutput.additionalContext` | ✔ for `startup`, `clear` and `compact`, and it is **fetched fresh from the daemon each time**: after `/compact` the model answered with the context file's new value | dynamic context (`tm context`, task, steps) |
| Bracketed paste (`ESC[200~…ESC[201~`), then Enter as a separate write 150 ms later | ✔ Multi-line text arrives as one prompt and Enter submits it | follow-ups (`paste` injector) |
| Paste while working | Queued (`Press up to edit queued messages`); delivered mid-turn after the next tool result | acceptable, but the server should wait for idle |
| **uds-messaging socket** | ✔ idle and mid-turn (queued). One NDJSON line `{"type":"user","message":{"role":"user","content":"…"}}` sent to `CLAUDE_CODE_MESSAGING_SOCKET` (also `messagingSocketPath` in the session file). An optional `{"type":"auth","token":…}` line first. **In this version a message without the token was accepted too** (2.1.291 may require it: §4a). The socket dir `/tmp/cc-socks` is mode 0700 (same user only). It is announced only in `--debug` output, i.e. undocumented | structured `channel` injector, behind a feature check |

**Re-injection after `/clear` and compaction:** map `SessionStart` with `source ∈ {clear, compact}`, and also `startup` and `resume`, to a `respond` template that returns `tm hook`'s context. Keep the static brief in `--append-system-prompt-file`. Two things to handle:
- `/clear` **changes the session id**, so update the stored agent session id from every `SessionStart`.
- The SPEC's assumption that the agent's own todo list is lost on `/clear` is correct: task ids restart at 1 in the new session.

**Paste hazards to handle in the injector:**
1. After an Esc cancel, the input box holds the restored prompt. Inject only when the prompt box is empty (ignoring dim ghost text), or clear it first. Ctrl+U was sent in s16 but its effect wasn't verified **(untested)**.
2. The prompt-suggestion ghost text looks like typed text unless cell attributes are checked.
3. Never paste while a dialog is open: an Enter would answer it.

The uds socket avoids all three, which is why it looked like the better injector. It isn't one for the human's prompts: they would arrive framed as from another session, slash commands don't run, and 2.1.291 can't tell the sender whether a message landed (§4a).

## 4a. The messaging socket in 2.1.291 (T47)

From the **2.1.291** binary's uds-messaging module (spike T47, thread t-0038; not yet confirmed live — `threads/t-0038/library/t47-probe.sh` in the project folder runs the cases by hand):

- **Wire:** one NDJSON line per message, `{"type":"user","message":{"role":"user","content":"…"}}`. Optional fields: `priority` (`now` jumps the queue, `next` is the default), `session_id` (a mismatch drops the message; `/clear` rotates it), `msg_id`, `from`, `file_attachments`.
- **Auth:** an optional `{"type":"auth","token":…}` first line. Whether it is required is decided at start-up per platform; when it is, lines from an unauthenticated connection are **dropped and the connection closed after the client's write already succeeded**. The token Claude puts in its children's env, `CLAUDE_CODE_MESSAGING_TOKEN`, is valid (the message may then count as `selfSent`). Hooks are children, so `tm hook` sees it.
- **No answer on the sending connection.** A successful connect and write means the inbox read the line, nothing more. Outcomes (`held` for approval, `denied`, `expired`, `delivered`, `refused`, `dropped` for rate limit, duplicate, relay loop or a full queue) go as `peer_message_status` receipts to the sender's own Claude-style inbox (`from: "uds:<path>"` under Claude's socket dir, pid-verified), which tm doesn't have. A message accepted at once gets no receipt.
- **Framing:** queued with `origin.kind = "peer"`, `isMeta: true` and **`skipSlashCommands: true`**: the model sees a peer message, not the user, and a slash command sent this way doesn't run.
- **Liveness:** Claude checks its peers with a plain connect (250 ms timeout) and closes a connection that sends no complete line. No socket file or a refused connection means the process is gone.

What tm does with it (SPEC §8.6, T51):
- `claude.toml` sets `inject.token_env = "CLAUDE_CODE_MESSAGING_TOKEN"`: `tm hook` hands the token to the server with each event, the session keeps it in memory only, and the channel writes the auth line first, in the same write as the message.
- The socket carries only tm's own fixed-word prompts once held for the paste injector's bound; a send is journaled **`prompt.sent`**, never "delivered". The human's and the coordinator's prompts are always pasted (the Claude adapter ignores `inject.prompt = "channel"`).
- The Claude adapter's `Probe` is a connect-only check, run every 5 s beside a pid check: a gone pid makes the status file stale, a gone socket keeps held prompts off the channel; `tm agent explain` shows `liveness`.
- Not used: a fake inbox under Claude's socket dir to receive receipts (it would imitate Claude internals). Prompts from the human should move to a mod's `$.prompt.submit` once that lands (T48/T49).

## 4b. Answering AskUserQuestion from a mod (2.1.291, T62)

Spike t-0051, live against **Claude Code 2.1.291** with a throwaway `--plugin-dir` mod and with terminatr's own:

- **Reading it.** `tool.call` with matcher `{ tool: 'AskUserQuestion' }` sees the tool's input: `questions[]` with `question`, `header`, `multiSelect` and `options[]` (`label`, `description`). `ui.render` on `AskUserQuestion` sees the same in `e.props.questions`, but that is a render site only: answering needs the tool call.
- **Answering it.** The hook calls `next(e)` (the engine draws the menu) and races it: returning `{ result: { questions, answers } }` while `next(e)` is pending takes the menu down, and the model reads `User answered Claude's questions: · <question> → <answer>` exactly as for a human answer. `answers` maps question text to a string: an option's label, labels joined with `, ` for a multi-select, or free text for the menu's `Type something.` Two questions in one call work the same.
- **The user first.** When the user answers in the pane, `next(e)` resolves with core's result (`answers`, `annotations`, and a `text` the model reads); the hook returns it unchanged and ends its `$.process.spawn` loop (`return()` on the iterator), which kills the child.
- **Not possible:** `$.tool.call({ tool: 'AskUserQuestion' })` from a plugin is refused (`that is $.ui.ask (host check)`); `$.ui.ask(question, { options, header, multiSelect })` opens one question and resolves to the label(s) or text, and its `tool.call` passes every other hook, so the probe used it to open menus without the model.
- **Hot reload:** editing a `--plugin-dir` mod's files reloads it at the next turn's end (`<plugin>: reloaded (…)`) with no question.

## 5. Other findings

- **Session ids:**
  - `--session-id <uuid>` works for a fresh session.
  - Reusing an existing id fails with `Error: Session ID … is already in use.` (exit 1), so a restart must use `--resume <id>`.
  - `--resume <id>` and `--continue` keep the id.
  - `--resume <id> --fork-session --session-id <new>` assigns our new id (`source=fork`).
  - `/clear` rotates the id; compaction keeps it.
  - `--resume ""` opens an interactive picker. Never pass an empty id.
- **`claude agents --json`** lists interactive and background sessions with `pid`, `sessionId`, `cwd`, `status` (`idle`/`busy`/`waiting`), `waitingFor` and `name`. It is the CLI face of the session files. Read the files directly instead of spawning the CLI every few hundred ms.
- **Statusline:** fields are `context_window`, `cost`, `model`, `rate_limits`, `session_id`, `transcript_path`, `workspace`, `version`, … with **no state field**. It ran 8 times in a short session. Setting it through `--settings` replaces the user's statusline, so **don't use it**. Context % and cost can come from the transcript later if wanted.
- **Headless** (`-p --output-format stream-json --verbose --include-hook-events`): every hook shows up as `system/hook_started:<Event>` and `system/hook_response:<Event>`, plus `system/init`, `assistant`, `system/notification`, `rate_limit_event` and `result/success`. That gives complete structured state, but no TUI. It fits the SPEC's non-goal (headless threads later). `-p` also skips the trust dialog.
- **Read-only project folder (SPEC open points 12 and 13)**, tested with `--settings` `{"permissions":{"allow":["Read(//P/**)"],"deny":["Edit(//P/**)"]},"sandbox":{"enabled":true,…}}`. In **interactive** mode and in **yolo** (`--dangerously-skip-permissions`) alike:
  - the Read ran silently with no dialog;
  - the Write tool was refused (`File is in a directory that is denied by your permission settings.`);
  - a Bash `echo > P/x` was refused by the sandbox (`operation not permitted`);
  - no file was created.
  So the deny rule and the sandbox both hold under yolo on macOS. Linux bubblewrap is still open.
- `TaskCreate` and `PostToolUse` hooks fired in yolo as well.

---

## 6. Todo mirroring

**Tool names in 2.1.289:** `TaskCreate`, `TaskUpdate` (also `TaskOutput`, which is for background task output, not todos). **There is no `TodoWrite`.** The tools are deferred (loaded through ToolSearch), but the hooks fire normally.

**Payloads (s16, s17).** Each call is a diff: one item per call, never the whole list.

```jsonc
// PreToolUse / PostToolUse, tool_name = "TaskCreate"
"tool_input":    {"subject": "alpha", "description": "Run echo step-alpha in bash", "activeForm": "Running alpha"},  // activeForm optional
"tool_response": {"task": {"id": "1", "subject": "alpha"}}                         // PostToolUse only: the assigned id
// PreToolUse / PostToolUse, tool_name = "TaskUpdate"
"tool_input":    {"taskId": "1", "status": "in_progress"}                          // also subject/description edits (inferred)
"tool_response": {"success": true, "taskId": "1", "updatedFields": ["status"],
                  "statusChange": {"from": "pending", "to": "in_progress"}}
// TaskCreated   (separate event)  {"task_id": "1", "task_subject": "alpha", "task_description": "…"}
// TaskCompleted (separate event)  {"task_id": "1", "task_subject": "alpha", "task_description": "…"}
```

- Statuses are `pending`, `in_progress` and `completed`, the same words as SPEC §7.3. There is no event for →`in_progress`; only `PostToolUse(TaskUpdate)` shows it.
- **Size:** full hook payloads were 0.8–1.0 KB. The `description` is free text, so a long one pushes a payload past 2 KB. That is irrelevant with stream sockets, but it is one more reason not to use datagrams. The trimmed envelope keeps `tool_input` and `tool_response` for these two tools.

**Reliability:**

| Case | Result |
|---|---|
| Every create and update | ✔ one `PreToolUse` + `PostToolUse` pair per call, plus `TaskCreated` / `TaskCompleted`. The tool calls are instant, so there is no "Pre without Post" gap |
| Esc mid-list (s18) | The list keeps its last state (k1 `in_progress`, k2 `pending`). The hook mirror and the task files agree. This is correct: that is also what Claude believes |
| `/clear` | New session id, **new empty list** (ids restart at 1). The mirror must start a new list on `SessionStart(source=clear)` |
| Compaction | The list survives (same session id; a `TaskUpdate` on id 1 worked after `/compact`) |
| yolo mode | Hooks fire |
| **Subagents** | **The subagent has no task tools.** It replied "TaskCreate and TaskUpdate are not available". So subagents never touch the list, and SPEC §8.6's "ignore subagent todo lists" is moot here |

**Fallback source:** `~/.claude/tasks/<session_id>/<id>.json`, one file per task, rewritten on every change:

```json
{"id": "1", "subject": "p1", "description": "Task p1", "status": "in_progress", "blocks": [], "blockedBy": []}
```

- The dir also holds `.lock` and `.highwatermark`.
- Files survived exit while tasks were still open.
- In s16, where every task ended `completed`, only `.highwatermark` was left afterwards. That suggests the list is cleared once it is all done, or at session end **(inferred)**.
- **Recommended:** mirror from hooks (instant, no file watching) and re-sync from this dir on `Stop` and `SessionStart`, to self-heal. The transcript also holds every `TaskCreate`/`TaskUpdate` `tool_use` block, so a third source exists for rebuilding after a daemon restart.

**Suggested manifest mapping.** SPEC §8.2's `[[todos]]` currently replaces the whole list from one array path. That fits `TodoWrite`-style tools (and probably Codex plans) but not Claude's diffs. Proposal: add an `op` field.

```toml
# Claude Code 2.1.x task tools: one item per call (diffs).
[[todos]]
op      = "upsert"                       # create or update one item by id
event   = "PostToolUse"
match   = { tool_name = "TaskCreate" }
id      = "tool_response.task.id"
text    = "tool_input.subject"
active_text = "tool_input.activeForm"    # optional; shown as the current item
status_default = "pending"

[[todos]]
op      = "upsert"
event   = "PostToolUse"
match   = { tool_name = "TaskUpdate" }
id      = "tool_input.taskId"
status  = "tool_input.status"            # absent → keep the stored status
text    = "tool_input.subject"           # absent → keep the stored text
status_map = { pending = "pending", in_progress = "in_progress", completed = "completed", deleted = "@remove" }  # "deleted": inferred

[[todos]]
op      = "reset"                        # start a new list
event   = "SessionStart"
match   = { source = "clear" }

[todos_snapshot]                         # optional resync source (generic: a dir of JSON files)
dir     = "{{.Home}}/.claude/tasks/{{.AgentSID}}"
glob    = "*.json"
id = "id"; text = "subject"; status = "status"
on      = ["Stop", "SessionStart"]

# A TodoWrite-style agent (whole list every call) keeps today's SPEC form:
# [[todos]]  op = "replace"  event = "PostToolUse"  match = { tool_name = "TodoWrite" }
#            list = "tool_input.todos"  text = "content"  status = "status"
```

---

## 7. Recommended adapter design

### 7.1 Generic: belongs to the interface or manifest format (SPEC §8)

These were all needed for Claude, but none of them is Claude-specific. Codex (rollout JSONL, app-server) and pi (extension events) have the same shapes.

| Capability | Form | Notes |
|---|---|---|
| Launch argv/env/files, resume/fork args, kickoff | data (`[launch]`) | **Add a rule: the kickoff must come before variadic flags or after `--`.** The template renderer should place `kickoff_args` last behind a `--` |
| Hook endpoint `tm hook --agent X` | core | Stream socket, bounded deadlines, trimmed envelope, exit 0, no stderr, prints only the server's response. Make the trim list manifest data (`hook_keep_fields`, plus `hook_keep_full = {tool_name=[…]}`) |
| Hook → state mapping with field matches | data (`[[hooks]]`) | As in SPEC §8.2 |
| **Background-activity counter** | data (new) | `[[hooks]] counter = "+bg"` / `"-bg"`, keyed by a payload field (`agent_id`). While the counter is above 0 after an idle signal, the state is working, reason `background`. Generic: Codex and pi have background tasks too |
| Ignore events by field | data (`ignore_fields`) | Must be finer than SPEC's "drop events with `agent_id`". Drop them **for state** but still feed the counter. And drop `SubagentStop` without `agent_type` (prompt suggestions) |
| Context re-injection | data (`respond` on a reset event) | As in SPEC; verified |
| Agent session id that changes mid-session | data (`session_field` read from **every** event, latest wins) | `/clear` rotates Claude's id. Resume must use the latest |
| **Status-file signal source** | data (new source type) | `path` template with `{{.PID}}` / `{{.AgentSID}}`, the JSON field for state, a `state_map`, and a `reason` field with a map. Watched with fsnotify plus a poll; generic in the core |
| **JSONL-tail signal source** | data (new source type) | Tail the file at a payload-supplied path (`transcript_path`), with match rules (field equals / text prefix) → signal. Covers Claude interrupts; Codex rollouts likewise |
| Todo mirroring with `replace` / `upsert` / `reset` ops and a snapshot dir | data (`[[todos]]` + `[todos_snapshot]`) | Section 6 above |
| Screen rules incl. pre-hook dialogs (trust) | data (`[[rules]]`) | Add `reason = "trust"`. Allow a rule to require a **cell attribute** (not dim) so ghost text isn't read as input; this needs libghostty cell attributes in `internal/detect` |
| Paste injector | core | Bracketed paste, a 150 ms gap, then Enter. Precondition: idle **and** prompt box empty (ignoring dim cells) **and** no dialog |
| Socket line injector | data (new injector kind) or Go | `inject.prompt = "socket"`. `path` comes from the status file field or a hook env, plus `line` templates (an auth line and a message line). Generic NDJSON-over-unix; whether it belongs in the core or Claude's Go is a judgement call (see below) |
| Arbitration (SPEC §8.4) | core | Add a source ranking: *status file > hooks > transcript > screen*, all per manifest. Plus "a visible blocker beats non-blocked". The 700 ms debounce applies only when the screen is the sole source |

### 7.2 Claude-specific (`manifests/claude.toml`, plus Go only if the core lacks a source type)

**As data:**
- **Launch:** `claude --plugin-dir <rt>/claude-plugin --settings <rt>/claude-settings.json --session-id <uuid> --append-system-prompt-file <brief> -- "<kickoff>"`. Resume: `--resume <latest agent sid>` (never empty) instead of `--session-id` and the kickoff. Yolo: `--dangerously-skip-permissions`.
- **Settings file:** `permissions.allow` `Read(//real/path/**)`, `permissions.deny` **`Edit(//real/path/**)`** (no `Write(...)`), `sandbox.enabled`, and `network.allowUnixSockets`: [server socket]. Never `statusLine`.
- **Plugin `hooks.json`**, all sync, timeout 5:
  - `SessionStart`, `UserPromptSubmit`, `PreToolUse`, `PostToolUse`, `PostToolUseFailure`, `PermissionRequest`, `PermissionDenied`
  - `Notification`, `Stop`, `StopFailure`, `SubagentStart`, `SubagentStop`, `TaskCreated`, `TaskCompleted`, `PreCompact`, `SessionEnd`
- **Hook → state:**

  | Event (match) | Signal |
  |---|---|
  | `UserPromptSubmit` | working |
  | `PreToolUse` / `PostToolUse` / `PostToolUseFailure`, no `agent_id` | working, transient |
  | `PermissionRequest` (`tool_name = AskUserQuestion`) | blocked / question |
  | `PermissionRequest` (other tools) | blocked / permission, carrying `tool_name` and `tool_use_id` |
  | `Notification` (`notification_type` = `permission_prompt` \| `elicitation_dialog` \| `agent_needs_input`) | blocked, late confirmation |
  | `Notification` (`idle_prompt`) | idle, late |
  | `Stop`, `StopFailure` | idle |
  | `SubagentStart` with `agent_type` | `+bg` (keyed by `agent_id`) |
  | `SubagentStop` with `agent_type` | `-bg` |
  | `SubagentStop` without `agent_type` | ignore |
  | `SessionEnd` with `reason` ≠ `clear` | exited |
  | `SessionStart` | idle + `respond` (context) + `session_field = session_id` |

- **Status file:** `~/.claude/sessions/{{.PID}}.json`. Field `status` maps `idle` → idle, `busy` → working, `waiting` → blocked. Field `waitingFor` maps `"permission prompt"` → permission, `"input needed"` → question. Also read `sessionId` and `messagingSocketPath`.
- **Transcript tail:** source `transcript_path`. A `type=user` entry whose text starts `[Request interrupted by user` → idle. `type=system, subtype=turn_duration` → turn end.
- **Todos:** the section 6 mapping.
- **Screen rules** (2.1.289):

  | State / reason | Match |
  |---|---|
  | blocked / permission | screen contains `Do you want to ` + `❯ 1. Yes` + `Esc to cancel` |
  | blocked / question | bottom 12 lines contain `Enter to select` + `to navigate` + `Esc to cancel` |
  | blocked / trust | `Yes, I trust this folder`, or `Yes, I accept` + `Bypass Permissions` |
  | working | title regex `^[◐◑◒◓\x{2800}-\x{28FF}] `; or bottom lines regex `^\s*[*·✢✳✶✻✽]\s+\S+…\s+\(` |
  | idle | title `^✳ ` (lowest priority, below the blockers); prompt box `^\s*❯` with no dialog |
  | unknown / skip | `showing detailed transcript` |

**Needs Go** (`internal/agent/claude`), only if the core does not grow the generic source types above:
1. The session-file watcher (pid → file, version guard).
2. The transcript interrupt detector.
3. The uds-messaging injector. I'd put this one in Claude's Go package even if the other two become generic: the protocol is undocumented, needs a feature probe (does the socket exist, does a test line get accepted), and must fall back to paste.

Everything else is data.

**What I'd decide (recommendation):**
- Add the two generic source types (status file, JSONL tail) to the core. Then Claude stays pure data except for the uds injector.
- Treat the session file as the primary state, with hooks for edges and details, and the screen for blockers and pre-hook dialogs.
- Pin `version` ranges in the manifest. `tm doctor` should warn when Claude's version is outside the tested range, because both the session file and uds-messaging are undocumented and could change in any release.

### 7.3 Answers to SPEC §8.6 / §14 open points

| # | Answer |
|---|---|
| 6 | Additive: `--plugin-dir`, `--settings` and project files all merge with the user's hooks |
| 7 | `SessionStart` `clear`/`compact` ✔, `additionalContext` honoured and fetched fresh each time ✔, `--append-system-prompt-file` survives `/clear` ✔ |
| 8 | Hooks are unreliable for Esc cases (three cases with no closing event). Use the session file, then the transcript, then the screen (section 2) |
| 9 | Screen strings in section 2. herdr's permission rules don't match this version. Paste + separate Enter ✔ |
| 10 | `TERMINATR_*` reaches hooks ✔ |
| 11 | No `TodoWrite`. `TaskCreate`/`TaskUpdate` diffs plus the `TaskCreated`/`TaskCompleted` events. Hooks fire in manual and yolo modes. Mapping in section 6 |
| 12 | Interactive matches `-p`: silent read, write refused by the deny rule (no dialog), Bash refused by the sandbox |
| 13 | Holds under yolo on macOS (Edit deny + sandbox). Drop `Write(...)` rules. Linux is still open |

## Not covered / open

- Linux (datagram limits differ; bubblewrap sandbox).
- `async: true` hooks.
- `PermissionDenied` (auto mode), `StopFailure` (API errors), MCP `Elicitation`.
- Auto-compaction (only `/compact` was tested).
- Several concurrent sessions sharing one daemon (no issue expected: the envelope carries the pane id).
- Ctrl+U clearing the input box.
- `skipDangerousModePermissionPrompt`.
- How the session file behaves when Claude crashes (stale `busy`?). Treat it as invalid when the pid is dead.
- herdr's `CHANGELOG.md` lines were not re-read; there was no herdr checkout locally. The stale cases come from the t-0001 notes and were reproduced here independently.
