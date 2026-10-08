# terminatr

**One terminal for all your coding agents: a coordinator you talk to, threads that do the work.**

You talk to one **coordinator**. It splits the work into tasks and hands each to a **thread**: an agent in its own git worktree that opens a pull request and reports back. `tm`, a single binary, keeps every agent running in a background server when you close the terminal, and its dashboard shows what each one is doing and what waits for you. All progress lives in plain markdown under `~/.terminatr/projects/<slug>/`, so clearing an agent's context loses nothing.

Claude Code is the first supported agent; others plug in through a manifest ([docs/SPEC.md §8](docs/SPEC.md#8-agents)).

```
 PROJECTS                    1 │ tm dashboard                                                                          ● server ok · 1 session
 ■ demo                    1   │ demo ────────────────────────────────────────────────────────────────────────────────────────────────────────
 └─ coordinator              · │  coordinator                               —           not running; enter starts it  1 inbox
    └─ T1 Fix the login  50% ○ │  T1 Fix the login                          ○ idle      report new  ▰▰▰▱▱  50% 1/2 ▸ Fix  2m  PR #7  t-0001
                               │        steps 1/2
                               │          ✓ 1 Reproduce
                               │          ○ 2 Fix
                               │        report new · for the coordinator
                               │  tasks: 0 need you · 1 in motion · 0 on deck
                               │ ≡ menu · enter attach · a project · t tasks · i inbox · , settings · ? help · prefix+q quit
                               │
```

## Requirements

macOS 13+ or Linux with glibc 2.28+ (arm64 or x86_64), git, and [Claude Code](https://claude.com/claude-code).

## Install

```sh
brew tap theclifmeister/terminatr https://github.com/theclifmeister/terminatr
brew trust --formula theclifmeister/terminatr/terminatr
brew install theclifmeister/terminatr/terminatr
```

Newer Homebrew refuses formulas from a tap it doesn't trust, hence `brew trust` once. No Homebrew? See [Install in docs/OPERATIONS.md](docs/OPERATIONS.md#install) for the direct download. Then run `tm doctor` to check the setup.

## First steps

1. Run `tm`. It starts the background server and opens the dashboard.
2. Press `n` to create a project, and `enter` on it to start its **coordinator**.
3. Tell the coordinator what you want. It plans tasks and starts threads; open one with `enter` to read along.
4. Press Ctrl+B then `d` to return to the dashboard. Sessions keep running when you close the terminal; `tm` brings you back. `?` lists the keys.

## Learn more

- [terminatr.dev](https://terminatr.dev): the website
- [docs/OPERATIONS.md](docs/OPERATIONS.md): using, installing, upgrading and operating `tm`
- [docs/SPEC.md](docs/SPEC.md): how it works
- [CONTRIBUTING.md](CONTRIBUTING.md): building from source, tests, pull requests
- [SECURITY.md](SECURITY.md): reporting a vulnerability

## Licence

[MIT](LICENSE) © 2026 Clifmeister.
