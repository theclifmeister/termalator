#!/usr/bin/env bash
# Which @imports in CLAUDE.md / CLAUDE.local.md reach the model in -p mode.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
R="$HOME/.termalator-spike"; P="$R/projects/demo"; WT="$R/worktrees/demo-t1"
"$HERE/setup.sh" >/dev/null
echo 'INSIDE-TOKEN: lime-5' > "$WT/.termalator/out/inside.md"
printf 'INLINE-TOKEN: fig-1\n@.termalator/out/inside.md\n@.termalator/brief.md\n@%s/CONTEXT.md\n' "$P" > "$WT/CLAUDE.local.md"
Q="Without using any tool, list every token of the form XXX-TOKEN: value that appears anywhere in your context. One line."
"$HERE/probe.sh" imp I3-local-mixed "$Q" --permission-mode manual
mv "$WT/CLAUDE.local.md" "$WT/CLAUDE.md"
"$HERE/probe.sh" imp I4-claudemd-mixed "$Q" --permission-mode manual
"$HERE/probe.sh" imp I5-claudemd-mixed-usersrc "$Q" --permission-mode manual --setting-sources user,project,local
"$HERE/probe.sh" imp I6-claudemd-adddir "$Q" --permission-mode manual --add-dir "$P"
"$HERE/probe.sh" imp I7-claudemd-bypass "$Q" --dangerously-skip-permissions
