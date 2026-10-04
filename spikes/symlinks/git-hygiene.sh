#!/usr/bin/env bash
# What git does with .termalator/ in a linked worktree: status, add -A, clean, worktree remove.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
"$HERE/setup.sh" >/dev/null
R="$HOME/.termalator-spike"; P="$R/projects/demo"; WT="$R/worktrees/demo-t1"; REPO="$R/repos/demo"
echo 'REPORT-TOKEN: plum-3' > "$WT/.termalator/out/REPORT.md"
step() { echo; echo "\$ $*"; "$@" 2>&1; echo "(exit $?)"; }

echo "## exclude file used by the linked worktree"
echo "git-dir=$(git -C "$WT" rev-parse --git-dir)"
echo "common-dir=$(git -C "$WT" rev-parse --git-common-dir)"
step git -C "$WT" rev-parse --git-path info/exclude
step git -C "$WT" status --short --ignored
step git -C "$WT" add -A
step git -C "$WT" status --short
step git -C "$WT" check-ignore -v .termalator/project/CONTEXT.md .termalator/out/REPORT.md

echo; echo "## without the exclude: what would git record?"
sed -i '' '/^\.termalator\/$/d' "$REPO/.git/info/exclude"
step git -C "$WT" status --short --untracked-files=all
step git -C "$WT" add -A --dry-run
echo '.termalator/' >> "$REPO/.git/info/exclude"

echo; echo "## git clean"
step git -C "$WT" clean -fd --dry-run
step git -C "$WT" clean -fdx --dry-run

echo; echo "## git worktree remove (no --force)"
step git -C "$REPO" worktree remove "$WT"
echo "worktree exists after remove: $(test -d "$WT" && echo yes || echo no)"
echo "project link: $(ls -l "$P/threads/t1/out" | awk '{print $NF}')"
printf 'project link readable: '; cat "$P/threads/t1/out/REPORT.md" 2>&1
echo "shared context intact: $(cat "$P/CONTEXT.md" | tail -1)"
