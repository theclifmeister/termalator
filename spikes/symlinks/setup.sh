#!/usr/bin/env bash
# Builds a throwaway Termalator layout under ~/.termalator-spike (see FINDINGS.md).
set -euo pipefail
ROOT="${SPIKE_ROOT:-$HOME/.termalator-spike}"
rm -rf "$ROOT"
P="$ROOT/projects/demo"
REPO="$ROOT/repos/demo"
WT="$ROOT/worktrees/demo-t1"

mkdir -p "$P/memory" "$P/threads/t1" "$REPO"
printf '# Context\nSECRET-CONTEXT-TOKEN: banana-42\n' > "$P/CONTEXT.md"
printf '# Tasks\n- [ ] T1 spike\n' > "$P/TASKS.md"
printf '# Memory\n- [prefs](memory/prefs.md)\n' > "$P/MEMORY.md"
printf 'MEMORY-TOKEN: kiwi-7\n' > "$P/memory/prefs.md"
printf '# Brief\nBRIEF-TOKEN: mango-9\n' > "$P/threads/t1/brief.md"

git -C "$REPO" init -q -b main
echo "# demo" > "$REPO/README.md"
git -C "$REPO" add . && git -C "$REPO" -c user.name=spike -c user.email=spike@example.invalid commit -qm init
git -C "$REPO" worktree add -q -b t1 "$WT"

# Shared context goes into the worktree; thread output lives in the worktree.
mkdir -p "$WT/.termalator/out/library"
ln -s "$P" "$WT/.termalator/project"
ln -sf "$P/threads/t1/brief.md" "$WT/.termalator/brief.md"
ln -s "$WT/.termalator/out" "$P/threads/t1/out"

# Keep it out of git: per-worktree exclude lives in the common dir's info/exclude.
EXCL="$(git -C "$WT" rev-parse --git-common-dir)/info/exclude"
grep -qx '.termalator/' "$EXCL" || echo '.termalator/' >> "$EXCL"
echo "project=$P"; echo "worktree=$WT"
