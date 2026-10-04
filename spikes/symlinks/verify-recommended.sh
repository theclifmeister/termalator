#!/usr/bin/env bash
# End-to-end check of the recommended layout: no .termalator/ in the worktree, a Read rule
# for the project folder, additionalDirectories scoped to the thread's own out/ dir.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
R="$HOME/.termalator-spike"; P="$R/projects/demo"; WT="$R/worktrees/demo-t1"
"$HERE/setup.sh" >/dev/null
rm -rf "$WT/.termalator" "$P/threads/t1/out"; mkdir -p "$P/threads/t1/out"
OUTD="$P/threads/t1/out"
TAIL=" Use only that one tool call. Then reply with one line: OK plus the result, or DENIED plus the exact error text."
for sb in off on; do
  S="{\"permissions\":{\"allow\":[\"Read(/$P/**)\"],\"additionalDirectories\":[\"$OUTD\"]},\"sandbox\":{\"enabled\":$([ $sb = on ] && echo true || echo false),\"autoAllowBashIfSandboxed\":true}}"
  l=recommended-sandbox-$sb
  "$HERE/probe.sh" $l V1-read-context "Use the Read tool on $P/CONTEXT.md and report the SECRET-CONTEXT-TOKEN.$TAIL" --permission-mode acceptEdits --settings "$S" &
  "$HERE/probe.sh" $l V2-bash-cat-memory "Use the Bash tool to run: cat $P/memory/prefs.md$TAIL" --permission-mode acceptEdits --settings "$S" &
  "$HERE/probe.sh" $l V3-write-report "Use the Write tool to create $OUTD/REPORT-$sb.md with content 'report $sb'.$TAIL" --permission-mode acceptEdits --settings "$S" &
  "$HERE/probe.sh" $l V4-bash-library "Use the Bash tool to run: mkdir -p $OUTD/library && echo lib-$sb > $OUTD/library/x-$sb.txt$TAIL" --permission-mode acceptEdits --settings "$S" &
  "$HERE/probe.sh" $l V5-write-tasks "Use the Read tool on $P/TASKS.md, then the Write tool to overwrite it with content 'hijacked $sb'. Use only those two tools. Then reply OK or DENIED plus the exact error text." --permission-mode acceptEdits --settings "$S" &
  "$HERE/probe.sh" $l V6-write-worktree "Use the Write tool to create notes-$sb.md in the current directory with content 'wt $sb'.$TAIL" --permission-mode acceptEdits --settings "$S" &
  wait
done
{ echo "TASKS.md: $(tail -1 "$P/TASKS.md")"; ls -R "$OUTD"; git -C "$WT" status --short; } > "$HERE/results/recommended-files.txt"
cat "$HERE/results/recommended-files.txt"
