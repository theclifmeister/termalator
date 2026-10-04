# Shared helpers for the Claude Code spike. Source from a scenario script.
#
# Each scenario gets a lab dir with a fresh git repo, a daemon (tmd) and a
# claude session in a private tmux server (-L tmspike), started with a clean
# environment so the outer herdr/supacode/orca variables don't leak in.

SPIKE=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
BIN=$SPIKE/bin
TMUX_L=tmspike
MODEL=${MODEL:-haiku}

lab_init() { # lab_init <name>
  NAME=$1
  LAB=$SPIKE/lab/$NAME
  rm -rf "${LAB:?}"; mkdir -p "$LAB/repo"
  git -C "$LAB/repo" init -q && echo "# $NAME" > "$LAB/repo/README.md" &&
    git -C "$LAB/repo" add . && git -C "$LAB/repo" commit -qm init
  SOCK=/tmp/tmspike-$NAME.sock   # keep it short: sun_path is 104 bytes on macOS
  CTXSOCK=/tmp/tmspike-$NAME.ctx
  EVENTS=$LAB/events.jsonl
  : > "$EVENTS"
}

daemon_start() { # daemon_start [extra tmd flags]
  "$BIN/tmd" -sock "$SOCK" -ctx "$CTXSOCK" -ctxfile "$LAB/context.md" -log "$EVENTS" "$@" &
  DAEMON_PID=$!
  sleep 0.2
}
daemon_stop() { kill "$DAEMON_PID" 2>/dev/null; wait "$DAEMON_PID" 2>/dev/null; rm -f "$SOCK" "$CTXSOCK"; }

# claude_start <session-id> [claude args...]; runs in $LAB/repo unless CWD set.
claude_start() {
  SID=$1; shift
  local cwd=${CWD:-$LAB/repo}
  local envs=(env -i HOME="$HOME" USER="$USER" LOGNAME="$USER" PATH="$BIN:$PATH"
    TERM=xterm-256color LANG=en_US.UTF-8 SHELL=/bin/zsh
    TERMALATOR_PANE_ID="$NAME" TERMALATOR_SOCK="$SOCK" TERMALATOR_CTX_SOCK="$CTXSOCK"
    TERMALATOR_CONTEXT_FILE="$LAB/context.md" TERMALATOR_HOOK_BIN="$BIN/tmhook"
    TERMALATOR_HOOK_TIMING="$LAB/timing.txt" TERMALATOR_FULL_PAYLOAD=1 ${EXTRA_ENV:-})
  tmux -L $TMUX_L kill-session -t "$NAME" 2>/dev/null
  tmux -L $TMUX_L new-session -d -s "$NAME" -x 140 -y 45 -c "$cwd" \
    "${envs[@]}" claude --model "$MODEL" ${SID:+--session-id "$SID"} "$@" \
    \; set-option -t "$NAME" remain-on-exit on
}

# sampler_start: log screen-classifier transitions to $LAB/screen.jsonl
sampler_start() { python3 "$SPIKE/scripts/sampler.py" $TMUX_L "$NAME" "$LAB/screen.jsonl" & SAMPLER_PID=$!; }
# std_start <sid> [args]: daemon + claude with the plugin + trust + sampler, wait for prompt
std_start() {
  daemon_start
  claude_start "$@" --plugin-dir "$SPIKE/plugin/termalator"
  trust; accept_bypass; sampler_start; wait_screen '^❯' 40 >/dev/null
}
keys() { tmux -L $TMUX_L send-keys -t "$NAME" "$@"; }
type_line() { tmux -L $TMUX_L send-keys -t "$NAME" -l "$1"; sleep 0.3; keys Enter; }
# paste <text>: bracketed paste, then Enter as a separate write.
paste() {
  printf '%s' "$1" | tmux -L $TMUX_L load-buffer -b tm -
  tmux -L $TMUX_L paste-buffer -p -d -b tm -t "$NAME"
  sleep "${PASTE_GAP:-0.15}"; keys Enter
}
screen() { tmux -L $TMUX_L capture-pane -p -t "$NAME"; }
pane_dead() { tmux -L $TMUX_L display -p -t "$NAME" '#{pane_dead}'; }
title() { tmux -L $TMUX_L display -p -t "$NAME" '#{pane_title}'; }

# wait_screen <regex> [timeout-s]: poll the visible screen until it matches.
wait_screen() {
  local re=$1 t=${2:-60} i=0
  while (( i < t * 4 )); do
    screen | grep -Eq -- "$re" && return 0
    sleep 0.25; i=$((i + 1))
  done
  echo "TIMEOUT waiting for /$re/" >&2; screen | tail -20 >&2; return 1
}
# wait_event <event-name> [count] [timeout-s]
wait_event() {
  local ev=$1 n=${2:-1} t=${3:-90} i=0
  while (( i < t * 4 )); do
    (( $(grep -c "\"hook_event_name\":\"$ev\"" "$EVENTS") >= n )) && return 0
    sleep 0.25; i=$((i + 1))
  done
  echo "TIMEOUT waiting for event $ev x$n" >&2; return 1
}
# accept the workspace trust dialog if it shows up
# The default option is "No, exit", so move down first. Hooks don't run until trust is accepted.
# Keys sent right after the dialog paints are dropped, so retry Down until the cursor is on Yes.
trust() {
  wait_screen 'Yes, I trust this folder' 10 2>/dev/null || return 0
  local i; for i in 1 2 3 4 5 6 7 8; do
    sleep 0.5; screen | grep -q '❯ Yes, I trust' && { keys Enter; return 0; }
    keys Down
  done
  echo "trust: could not select Yes" >&2
}

# --dangerously-skip-permissions shows a one-time warning whose default is also "No, exit".
accept_bypass() {
  wait_screen 'Yes, I accept' 6 2>/dev/null || return 0
  local i; for i in 1 2 3 4 5 6 7 8; do
    sleep 0.5; screen | grep -q '❯ Yes, I accept' && { keys Enter; return 0; }
    keys Down
  done
}

# events: one line per hook event, relative ms, latency, key fields
events() {
  python3 "$SPIKE/scripts/events.py" "$EVENTS"
}
snap() { # snap <label>: save the screen and title to the lab dir
  { echo "== $1  title=[$(title)]"; screen; } >> "$LAB/screens.txt"
}
cleanup() { kill $SAMPLER_PID 2>/dev/null; tmux -L $TMUX_L kill-session -t "$NAME" 2>/dev/null; daemon_stop; }
