# Spike: libghostty-vt from Go (client/server pane host)

Spike for Termalator thread t-0003, written 2026-10-04. This is throwaway-quality
code. The point is the findings.

**Verdict: go ahead with libghostty-vt via cgo.** The client/server shape that
the user asked for works end to end with Claude Code 2.1.289:

- A detached server (setsid, no controlling tty) owns the PTY, the agent and
  the authoritative emulator.
- Clients attach over a unix socket. Each one receives a binary snapshot, then
  the ordered byte stream, and forwards input and resize.
- Detaching mid-stream, closing the client's terminal window outright, and
  reattaching from a new window all leave Claude running. The reattached
  screen matches the server exactly, verified by a full-state digest at 21
  points across the three final Claude runs (most of them mid-stream), with
  0 mismatches.
- The build is easy with a pinned Zig, and the result is a single static
  binary on macOS and Linux.

The main caveats:

- **Version coupling.** The Go API and the snapshot format are both unstable.
- **Mouse and focus forwarding are required.** Claude Code is now full-screen
  by default.
- **Avoid resizing the PTY on attach.** Claude's *inline* mode duplicates
  rows in scrollback whenever it is resized.

## What was built

All of it lives in `spikes/libghostty/`, with its own `go.mod`. The repo root is untouched.

| Piece | File | What it does |
|---|---|---|
| Build script | `scripts/build-libghostty.sh` | Downloads Zig 0.16.0 into `.deps/`, checks out Ghostty at the pinned commit, and runs `zig build -Demit-lib-vt`. `TARGET=aarch64-linux-gnu` cross-builds |
| Server | `cmd/gvt/server.go` | PTY (creack/pty), the libghostty `Terminal` that owns the answers to terminal queries, the unix socket, snapshot-on-attach, per-client byte-bounded queues with snapshot **resync** for slow clients, and stripping of inherited Claude session environment variables |
| Client | `cmd/gvt/client.go`, `render.go`, `input.go`, `mouse.go` | A **mirror** emulator restored from the snapshot and then fed the same stream. Dirty-row cell renderer to the outer terminal, wrapped in mode 2026. Input decoded with ultraviolet and **re-encoded with libghostty's key, mouse, focus and paste encoders** against the mirror's modes. Shift+PgUp/PgDn scrolls a per-client scrollback. Ctrl+\ detaches |
| Protocol | `cmd/gvt/proto.go` | `[type u8][len u32][payload]`. The server sends snapshot, output, resize, digest and exit; the client sends input, set-size and digest-request. Output and resize travel **in one ordered stream**, so the mirror applies them at the same byte offset as the server |
| Harness | `cmd/harness/` | Plays the user's terminal window: it runs `gvt` in a PTY and parses that output with *another* libghostty terminal (the "outer screen"). It sends keys encoded the way Ghostty would, using the kitty flags the client pushed, sends mouse wheel and bracketed paste, resizes, and closes the window. It compares the outer screen with the server's screen (`gvt dump`) and the mirror with the server (in-stream digest) |
| Tests and benchmarks | `cmd/gvt/*_test.go` | Snapshot-then-resize equivalence, VT-replay-as-snapshot, `VTWrite`, frame and snapshot benchmarks |

Commands: `gvt new -s SOCK -- claude` spawns a detached server and attaches.
`gvt attach -s SOCK` attaches. `gvt dump -s SOCK [-all|-vt]` prints the
server's screen. `gvt server ...` runs the server in the foreground.

## 1. Build: toolchain, steps, linking, size (macOS)

**Machine.** macOS 27.0.1 (26A434) on arm64, Apple clang 21.0.0, Go 1.27.1, Zig **0.16.0** (official tarball; see below), pkgconf 3.0.7 (`brew install pkgconf`; it was not installed).

**Pinned versions:**

- Ghostty `33da6848d63b` (2026-10-01, `v1.3.1-2830`). This is the commit that go-libghostty's `CMakeLists.txt` pins, and `build.zig.zon` declares `minimum_zig_version = "0.16.0"`. Homebrew ships Zig 0.17.0, which was not tried.
- go-libghostty `76867c77a212` (2026-10-01).
- creack/pty v1.1.24.
- ultraviolet `878653296cfd`.
- Claude Code 2.1.289 (Haiku 4.5 for the streaming tests).

**Steps:**
```sh
cd spikes/libghostty
./scripts/build-libghostty.sh            # zig build -Demit-lib-vt -Demit-xcframework=false -Doptimize=ReleaseFast --prefix .deps/ghostty-out
export PKG_CONFIG_PATH=$PWD/.deps/ghostty-out/share/pkgconfig
go build -o bin/gvt ./cmd/gvt            # static by default; -tags dynamic for the dylib
go build -o bin/harness ./cmd/harness
./bin/harness shell                      # cheap end-to-end test (zsh)
./bin/harness -cwd <dir> claude          # full-screen Claude; also: claude-inline, -samesize claude-inline, bench
```
- The cold build took 54 s on this Mac: Zig download, a `--filter=blob:none` clone and `zig build`. Neither CMake nor Xcode is needed; go-libghostty's Makefile wraps CMake, but calling `zig build` directly is enough.
- **The Zig build fetches packages over the network** (aro, translate_c, uucode, highway, simdutf and others) into `~/.cache/zig` (112 MB). CI needs to cache or vendor them; go-libghostty's own CI retries on flaky `deps.files.ghostty.org` fetches.
- Disk use: Zig 406 MB, Ghostty checkout 522 MB.
- go-libghostty links through `#cgo pkg-config: --static libghostty-vt-static`, so **pkg-config is a hard build dependency**.
- **`CGO_ENABLED=0` does not compile** (`key_string.go: undefined: Key`). A pure-Go fallback build would have to keep the import behind a build tag.

| Artifact | Size |
|---|---|
| `libghostty-vt.a` / `.dylib` (macOS arm64) | 11.0 MB / 1.9 MB |
| hello-world, static / dynamic | 7.8 MB / 3.0 MB (plus the 1.9 MB dylib) |
| `gvt`, static / static stripped (`-s -w`) / dynamic | 11.9 MB / **6.7 MB** / 7.2 MB |

- The static binary links only `libSystem` and `libresolv` (`otool -L`). That makes it a **single-file** distribution, with no runtime library path issues.
- The dynamic build needs `DYLD_LIBRARY_PATH` or an rpath fix-up.
- **Recommendation: link statically.** Notarization or code signing for direct downloads is the usual macOS chore, and cgo does not change it.

**Linux.** Tested by cross-compiling from the Mac and running in Docker (linux/arm64, Debian `golang:1.26` image):
```sh
TARGET=aarch64-linux-gnu ./scripts/build-libghostty.sh       # 28 s with a warm cache
CGO_ENABLED=1 GOOS=linux GOARCH=arm64 CC="zig cc -target aarch64-linux-gnu" \
  PKG_CONFIG_PATH=$PWD/.deps/ghostty-out-aarch64-linux-gnu/share/pkgconfig go build ./cmd/gvt
```
- The shell scenario and the bench pass on Linux, including closing the window: the client exits 52 ms after the master closes, and the server and shell survive.
- The binary is 20.6 MB (16.1 MB with `-s -w`; the Linux `.a` is 18.9 MB). It links **glibc dynamically** (libc, libresolv, libpthread, librt).
- For release builds, either pin the glibc floor in the Zig target (for example `x86_64-linux-gnu.2.28`) or try `*-linux-musl` for a fully static binary. musl was not tested.
- Claude itself was not run on Linux, because the container has no Claude login.
- The Linux shell test needs a UTF-8 locale (`LANG=C.UTF-8`). Otherwise readline mangles multibyte input, which is an environment issue, not a host bug.

## 2. The pane host with Claude Code

The Claude runs used `claude --model haiku --tools "" --strict-mcp-config`, in a fresh folder. The first-run folder-trust dialog is handled by the harness. Results are in `test-results/`.

**Claude Code 2.1.289 is full-screen by default.** It enters the alt screen (1049), turns on any-event mouse tracking with SGR coordinates (1000, 1002, 1003, 1006), focus events (1004), bracketed paste (2004), colour-scheme updates (2031) and the kitty keyboard protocol. It scrolls its own transcript with the mouse wheel. The old inline renderer is still available with `CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN=1`, and both modes were tested.

| Check | Full-screen (default) | Inline |
|---|---|---|
| Startup, prompt and status screens: outer == server | ✅ | ✅ |
| **Shift+Enter** inserts a newline. The outer terminal sends `CSI 13;2u`, and the client re-encodes it as `CSI 13;2u` because Claude negotiated kitty flags | ✅ | ✅ |
| **Ctrl+Enter** arrives as `CSI 13;5u`, and Claude **submits** the prompt. This is Claude's binding, not a host bug | ✅ (delivered) | ✅ (delivered) |
| **Bracketed paste**, multi-line: lands in the prompt and is not submitted | ✅ | ✅ |
| **Wide characters / emoji** (CJK, skin-tone, flag, ZWJ family, combining marks): outer == server | ✅ | ✅ |
| **Mode 2026**: nearly every Claude frame is a synchronized update (≈ 1 hold per frame). The client renders the frame captured at hold start and never a torn one; 0 hold timeouts | ✅ | ✅ |
| **Resize** 90×30 and 140×45 while idle: outer == server | ✅ | ✅ |
| Scrollback: mouse wheel goes to Claude (full-screen); client-local Shift+PgUp (inline) | ✅ | ✅ |
| No ghost or duplicated rows on any captured screen | ✅ | ⚠️ only after a resize (below) |
| Every streamed row appears exactly once in screen plus scrollback | n/a (alt screen) | ✅ with no resize, ❌ with resizes |

**Inline mode duplicates rows when it is resized.** This is Claude's behaviour, proven from the raw PTY bytes in `claude-pty-raw.bin`:

- On SIGWINCH, Claude moves the cursor home, erases the visible rows (`CSI 2K` ×N), and repaints its live region starting from, say, `row 73`.
- Rows that had already scrolled into scrollback cannot be erased, so they appear twice.
- The emulator reproduces those bytes faithfully: the mirror and server digests match.
- With the same window size on reattach (`-samesize`), there are **0 duplicates and 0 missing rows** out of 300.

Two consequences for Termalator:

- Don't resize the PTY on every attach. Keep the pane at a stable size, and only resize when the user really changes it.
- Full-screen mode, which is now the default, doesn't have this problem.

**Ghost lines in general:** none were found. Every captured screen shows consecutive rows. The outer screen equals the server screen at every comparison point in all scenarios. The earlier "mismatches" all came from my own test tooling, not the emulator.

## 3. Client/server: detach, reattach, window close (the user's requirement)

These steps ran mid-stream while Claude printed 300 lines, in all three Claude variants and in the zsh scenario:

1. Window A attaches at 120×40. Claude starts streaming.
2. About 2 s in, A detaches with **Ctrl+\\** (sent as `CSI 92;5u`). The server and Claude stay alive, and the server's screen keeps advancing (for example from `row 20` to `row 102` within 2 s) with no client attached.
3. Window B attaches **at a different size (100×32) while Claude is still streaming**. It gets a snapshot (1–10 KB, decoded in 60–240 µs) and then the stream. Five in-stream digest checks pass: the mirror equals the server, including every mode, both screens and scrollback.
4. **Window B is closed outright.** The PTY master disappears, the client sees EOF or EIO on stdin (macOS) or SIGHUP (Linux) and exits within 50 ms. The server and Claude are unaffected; they are in their own session with no controlling tty.
5. Window C attaches from a fresh PTY after the stream ends. It gets the full snapshot, the outer screen equals the server screen, and the digest matches.
6. When Claude exits, the server exits and removes its socket.

**Why the mirror design works well:**

- Each client keeps its own libghostty instance, restored from the snapshot and then fed the identical stream.
- The server stays cheap: it parses once, then forwards bytes.
- Key, mouse and paste encoding read the mirror's modes, so there is no round trip.
- Per-client scrollback and viewport come for free.
- Late joiners and lagging clients get a fresh snapshot.

**Things the real implementation must do (found the hard way):**

- **Only the server's emulator may answer terminal queries.** Wire the write-pty effect only on the server; leave it unset on mirrors. Otherwise DA, DSR and kitty queries are answered once per client.
- **Bind the socket before spawning the agent.** Otherwise a bind failure leaves an unreachable agent running.
- **macOS limits a unix socket path to 104 bytes.** A path under a long `~/.termalator/projects/<slug>/` can exceed that. Keep sockets in a short runtime directory (for example `$TMPDIR/termalator-$UID/<id>.sock`) and check the length.
- **Strip inherited Claude session variables** from the agent environment. Without this, a Claude launched from inside another Claude believes it is a child session (for example, transcript saving is turned off). herdr does the same. Strip `CLAUDECODE`, `CLAUDE_CODE_SESSION_ID`, `CLAUDE_CODE_CHILD_SESSION`, `CLAUDE_CODE_MESSAGING_*`, `CLAUDE_CODE_ENTRYPOINT` and similar.
- **Never let a slow client block or drop.** The first version used an 8192-message channel per client. macOS PTYs deliver about 68-byte reads, so the queue overflowed in 1.4 s under load. The fix: per-client byte-bounded queues (4 MB), with adjacent output frames merged, and past the limit the backlog is replaced by a fresh snapshot (resync).
- **The client must treat SIGHUP and stdin EOF or EIO as detach.** It must also paint the snapshot right away, even when the pane is idle. I found that bug on a same-size reattach to an idle pane.
- **Forward mouse and focus.** Mirror the app's 1000, 1002, 1003 and 1004 settings onto the outer terminal only while the app wants them, so native selection works the rest of the time. Encode with libghostty's mouse encoder.
- **Re-anchor the cursor after multi-codepoint graphemes** (ZWJ sequences, flags, skin tones, VS16). Outer terminals disagree on their width, and without a re-anchor one disagreement shifts the rest of the row. This was tested only against a libghostty outer terminal, not Terminal.app or iTerm2.
- **Wire up the remaining effects.** Claude sends `CSI 16t` (cell size) and DA1 on resize, and uses 2031 (colour scheme). The spike does not register `WithSizeReport` or `WithColorScheme`. That is a small TODO, but the replies matter for exact image and colour behaviour.

## 4. Performance

| Measurement | Result |
|---|---|
| Claude streaming 300 lines (≈ 25–60 KB of PTY output over about 11 s) | Server CPU **20–40 ms** in total (< 0.3 %). Each client 10–20 ms. Claude itself 0.55–1.05 s |
| PTY output → frame on screen (client) | p50 **0.1–0.2 ms** when idle-driven, p95 ≤ 7 ms. The 120 Hz frame cap adds up to 8.3 ms under continuous output |
| Keystroke → echo arriving from Claude | p50 **≈ 1 ms**, p95 1.7–3.2 ms |
| Frame build (dirty rows only) | p50 0.09–0.2 ms. A full 160×50 repaint takes 1.5 ms (per-cell cgo calls) |
| Client resident memory | 15–17 MB |
| Snapshot encode + decode, 160×50 with 10k scrollback rows | 68 µs (4.6 KB) |
| libghostty `VTWrite` alone | **250–280 MB/s**, roughly the same for 64-byte and 64 KB chunks |
| Firehose: `seq 1 2000000` through server and client, macOS | 20.8 MB in 2.1 s. Server CPU 3.3 s, client 1.4 s. 305k PTY reads averaging 68 B |
| Same firehose, Linux | 16.9 MB in 0.30 s. Server CPU 0.37 s, client 0.24 s. 10.8k reads averaging 1.6 KB |

**Conclusions:**

- Claude-level output costs nothing measurable.
- Under a firehose, the cost is my per-read Go plumbing (a message, a lock and a write per tiny macOS PTY read), not libghostty.
- The real server should coalesce reads (keep reading until EAGAIN, or for a few hundred µs) and avoid allocating per chunk.
- The renderer could switch to `CellsRaw` and `StyleInto` if full repaints ever matter.

## 5. API stability and risk

- **go-libghostty says outright: "I'm not promising any API stability yet."** Pin it together with the Ghostty commit it was built against; its `CMakeLists.txt` pins `33da6848`. Upgrade the two as a pair.
- The C header says **snapshot format v1 "is a work in progress and does not yet carry a binary-compatibility guarantee."**
  - With the mirror design, client and server must be the **same build**. The protocol needs a version handshake on attach. On a mismatch, tell the user to restart the client, or re-exec it from the server's binary path.
  - I tested the obvious version-independent fallback: replaying the VT formatter output (with modes, cursor, keyboard and other extras) into a fresh terminal. It reproduces the **active** screen and modes exactly. It **loses the inactive screen**: the primary scrollback (171 → 0 rows) and its kitty flags when the app is on the alt screen. So it is a lossy fallback, not a replacement (`TestVTReplayAsSnapshot`).
- A snapshot-restored terminal does not carry Go callbacks. Effects (render hold, write-pty) must be registered again after `Decode`. Resize and reflow after restore behave identically to the original (`TestSnapshotThenResizeMatches`).
- **Daemon upgrades** are not solved. The server binary holds the PTYs, so a new version requires either draining the old server or a handoff: pass the PTY master over `SCM_RIGHTS` and send a snapshot to the new server. Snapshots make that handoff feasible, but it is untested and worth a small follow-up spike.

## 6. Recommendation for the real implementation

1. **Emulator:** libghostty-vt through go-libghostty, **statically linked**. Pin the Ghostty and go-libghostty commits together. Build with `zig build -Demit-lib-vt` and Zig 0.16.0 from a script or Make target (no CMake needed). Cache `~/.cache/zig` and the per-target `ghostty-out*` in CI. Cross-compile Linux with `zig cc`, and pin the glibc floor or try musl.
2. **Architecture:** keep this spike's shape.
   - A setsid'd server per user (or per project) owns the PTYs, the agents and an authoritative emulator, and is the only one that answers terminal queries.
   - Clients mirror the emulator from snapshot plus an ordered stream, and render and encode input locally.
   - Add a version handshake. Use byte-bounded queues with snapshot resync. Use an in-stream digest as a debug or consistency check (it found nothing wrong here, but it was invaluable for clearing the emulator while debugging the harness).
3. **Sizing policy:** never resize the PTY just because a client attached. Use the active client's size, change it only on a real resize, and render other clients cropped or padded. This avoids the inline-mode duplication and pointless repaints.
4. **Input:** decode the outer terminal with ultraviolet. Push kitty "disambiguate" on the outer terminal so Shift+Enter, Ctrl+Enter and Ctrl+\\ are distinguishable. Re-encode with libghostty's key, mouse, focus and paste encoders against the pane's modes. Mirror the mouse and focus modes onto the outer terminal only while the app wants them.
5. **Rendering:** a cell renderer with dirty rows, frame-capped (120 Hz is fine) and wrapped in 2026, with palette and default colours kept symbolic so the user's theme applies. Honour the app's 2026 holds through the render-hold effect, and re-anchor after multi-codepoint graphemes. The Bubble Tea dashboard can sit next to it; the pane view should not go through string `View()`s.
6. **Housekeeping:** strip inherited agent session variables, keep sockets in a short runtime directory, bind before spawning, and wire size-report, colour-scheme and the other effects.
7. **Before v0.1, still to verify:**
   - Codex and pi under the same harness (it is reusable: point it at another argv).
   - A real outer terminal other than libghostty (Ghostty.app, iTerm2, Terminal.app) for grapheme widths and kitty keyboard fallbacks.
   - Claude on Linux.
   - The daemon upgrade handoff.

## Reproducing

```sh
cd spikes/libghostty
./scripts/build-libghostty.sh && export PKG_CONFIG_PATH=$PWD/.deps/ghostty-out/share/pkgconfig
go test ./cmd/gvt && go test ./cmd/gvt -run XXX -bench .
go build -o bin/gvt ./cmd/gvt && go build -o bin/harness ./cmd/harness
./bin/harness -out results shell
./bin/harness -out results-bench bench
mkdir -p .deps/claude-cwd
./bin/harness -out results-claude -cwd $PWD/.deps/claude-cwd claude                 # spends a few Haiku tokens
./bin/harness -out results-claude-inline -cwd $PWD/.deps/claude-cwd claude-inline
./bin/harness -samesize -out results-claude-inline-same -cwd $PWD/.deps/claude-cwd claude-inline
# Try it by hand:
./bin/gvt new -s $TMPDIR/t.sock -- claude      # Ctrl+\ detaches; ./bin/gvt attach -s $TMPDIR/t.sock
```
The `test-results/` folder holds the result logs, per-client stats and selected screen captures from the runs on 2026-10-04.
