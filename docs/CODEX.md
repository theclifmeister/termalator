# Using Codex

terminatr runs [Codex](https://developers.openai.com/codex) next to Claude Code: as a coordinator, as threads, or both. This page covers what differs. The rest of tm works the same.

## Set up

1. Install Codex and log in with your ChatGPT account (`codex login`). Run `tm doctor` to check that tm finds it.
2. Choose the agent in a project's popup: `a` on the project, then **Settings**. **Thread agent** sets the agent for new threads, **Coordinator agent** the one for a new coordinator (a running coordinator keeps its agent until it is started anew). Both can be set for all projects too. In `config.toml` they are `thread_agent` and `coordinator_agent`.
3. For one thread, name the agent instead: `tm thread start --agent codex "title"`.

## Models

Codex threads run `gpt-6-luna` unless a model is chosen; `gpt-5.6-terra` is the other. `gpt-6-astra` is refused on a ChatGPT login. Pick one with `--model` (`tm thread start --agent codex --model gpt-5.6-terra …`). If the project limits its models (**Thread models** in Settings, or `models` in `config.toml`), the list must include the Codex models, or `--model` is refused with `model-not-allowed`. Without `--model` the default runs and isn't checked. When OpenAI releases another model, add it (or change the default) in Settings > General > **Models**; no tm release is needed.

## Approvals and the sandbox

- A thread asks for approval (`-a on-request`). The coordinator approves the in-scope ones with `tm thread approve`; the rest reach you in the dashboard. A coordinator runs on Codex's own auto-review.
- A thread's sandbox: its worktree is writable; the project folder and `config.toml` are read-only; the network is open (tm's socket needs it, which also opens outbound HTTPS). A thread started in yolo mode has no sandbox.
- The guard covers Codex: a refused command (a PR merge, a write outside the worktree) is denied before any approval.

## What you'll see

- Prompts you or the coordinator send reach Codex through `codex queue`, so they show up as your own prompt in its terminal: at once when it is idle, after the current turn when it is busy. Slash commands can't be sent that way. After `/clear` (`/new`, `/resume`, `/fork`), sent by tm or typed in the pane yourself, prompts are pasted until the new thread's first prompt reports its id, so none goes to the thread you left.
- Usage shows the plan limit used (a percentage) rather than a cost.
- Codex keeps its sessions under `~/.codex`. tm adds nothing there; its settings are passed on the command line.
- If you have your own Codex hooks that are untrusted, tm skips them in its sessions instead of stopping at Codex's review screen.

## For contributors

`make test-codex` runs the end-to-end scenarios against the real `codex` and costs a little usage. Keep its sessions out of `~/.codex` with a home of its own, logged in once:

```sh
CODEX_HOME=~/.codex-tm-test codex login
CODEX_HOME=~/.codex-tm-test make test-codex
```

How it works: [SPEC.md §8.6, Codex](SPEC.md#8-agents).
