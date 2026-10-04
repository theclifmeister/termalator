#!/usr/bin/env bash
# The reverse direction: the coordinator runs in the project folder and reads a
# thread's report through threads/t1/out -> <worktree>/.termalator/out.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
P="$HOME/.termalator-spike/projects/demo"
TAIL=" Use only that one tool call. Then reply with one line: OK plus the result, or DENIED plus the exact error text."
"$HERE/setup.sh" >/dev/null
echo 'REPORT-TOKEN: plum-3' > "$P/threads/t1/out/REPORT.md"
for l in manual acceptEdits; do
  PROBE_CWD="$P" "$HERE/probe.sh" coord-$l C1-read-thread-report "Use the Read tool on threads/t1/out/REPORT.md and report the REPORT-TOKEN.$TAIL" --permission-mode $l &
  PROBE_CWD="$P" "$HERE/probe.sh" coord-$l C2-bash-cat-report "Use the Bash tool to run: cat threads/t1/out/REPORT.md$TAIL" --permission-mode $l &
done
PROBE_CWD="$P" "$HERE/probe.sh" coord-adddir C1-read-thread-report "Use the Read tool on threads/t1/out/REPORT.md and report the REPORT-TOKEN.$TAIL" --permission-mode acceptEdits --add-dir "$HOME/.termalator-spike/worktrees" &
wait
