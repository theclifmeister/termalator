#!/usr/bin/env bash
# Runs every probe under each permission configuration; resets the layout per config.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
P="$HOME/.termalator-spike/projects/demo"
SANDBOX='{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true}}'
TAIL=" Use only that one tool call, nothing else. Then reply with one line: OK plus the result, or DENIED plus the exact error text."

probes() { # label, extra args...
  local l=$1; shift
  "$HERE/setup.sh" >/dev/null
  {
  "$HERE/probe.sh" "$l" R1-read-symdir "Use the Read tool on .termalator/project/CONTEXT.md and report the SECRET-CONTEXT-TOKEN.$TAIL" "$@" &
  "$HERE/probe.sh" "$l" R2-read-symfile "Use the Read tool on .termalator/brief.md and report the BRIEF-TOKEN.$TAIL" "$@" &
  "$HERE/probe.sh" "$l" R3-bash-cat "Use the Bash tool to run: cat .termalator/project/memory/prefs.md$TAIL" "$@" &
  "$HERE/probe.sh" "$l" R4-read-abs-outside "Use the Read tool on $P/TASKS.md (absolute path).$TAIL" "$@" &
  "$HERE/probe.sh" "$l" W1-write-out "Use the Write tool to create .termalator/out/REPORT.md with content 'report $l'.$TAIL" "$@" &
  "$HERE/probe.sh" "$l" W2-write-symdir "Use the Write tool to create .termalator/project/w2.md with content 'w2 $l'.$TAIL" "$@" &
  "$HERE/probe.sh" "$l" W3-bash-out "Use the Bash tool to run: echo 'status $l' > .termalator/out/STATUS.md$TAIL" "$@" &
  "$HERE/probe.sh" "$l" W4-bash-symdir "Use the Bash tool to run: echo 'w4 $l' > .termalator/project/w4.md$TAIL" "$@" &
  "$HERE/probe.sh" "$l" W5-write-abs-outside "Use the Write tool to create $P/w5.md with content 'w5 $l'.$TAIL" "$@" &
  "$HERE/probe.sh" "$l" E1-edit-symfile "Use the Read tool on .termalator/brief.md, then the Edit tool to replace mango-9 with mango-edited in that same path. Use only those two tools.  Then reply OK or DENIED plus the exact error text." "$@" &
  wait
  }
  # Ground truth from the filesystem, independent of what the model says.
  { echo "## files after $l"
    for f in "$P/w2.md" "$P/w4.md" "$P/w5.md" "$P/threads/t1/out/REPORT.md" "$P/threads/t1/out/STATUS.md"; do
      printf '%s: %s\n' "${f#$HOME/}" "$(cat "$f" 2>/dev/null || echo MISSING)"; done
    printf 'brief: %s\n' "$(grep TOKEN "$P/threads/t1/brief.md")"
    printf 'brief still symlink: %s\n' "$(test -L "$HOME/.termalator-spike/worktrees/demo-t1/.termalator/brief.md" && echo yes || echo NO)"
  } > "$HERE/results/$l/files.txt"
}

want() { [ "${1}" = "$sel" ] || [ "$sel" = all ]; }
sel="${1:-all}"
want manual && probes manual --permission-mode manual
want acceptEdits && probes acceptEdits --permission-mode acceptEdits
want sandbox && probes sandbox-acceptEdits --permission-mode acceptEdits --settings "$SANDBOX"
want adddir && probes adddir-acceptEdits --permission-mode acceptEdits --add-dir "$P"
want adddir-sandbox && probes adddir-sandbox --permission-mode acceptEdits --add-dir "$P" --settings "$SANDBOX"
want auto && probes auto --permission-mode auto
want bypass && probes bypass --dangerously-skip-permissions
want bypass-sandbox && probes bypass-sandbox --dangerously-skip-permissions --settings "$SANDBOX"
READONLY="{\"permissions\":{\"allow\":[\"Read(/$P/**)\"]}}"
READONLY_SB="{\"permissions\":{\"allow\":[\"Read(/$P/**)\"]},\"sandbox\":{\"enabled\":true,\"autoAllowBashIfSandboxed\":true}}"
want readonly && probes readonly-acceptEdits --permission-mode acceptEdits --settings "$READONLY"
want readonly-sandbox && probes readonly-sandbox --permission-mode acceptEdits --settings "$READONLY_SB"
want settings-adddir && probes settings-additionalDirectories --permission-mode acceptEdits --settings "{\"permissions\":{\"additionalDirectories\":[\"$P\"]}}"
WT="$HOME/.termalator-spike/worktrees/demo-t1"
READBOTH="{\"permissions\":{\"allow\":[\"Read(/$P/**)\",\"Read(/$WT/.termalator/**)\"]}}"
want readonly-both && probes readonly-both --permission-mode acceptEdits --settings "$READBOTH"
true
