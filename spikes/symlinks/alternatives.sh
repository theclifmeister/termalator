#!/usr/bin/env bash
# Alternatives to directory symlinks: hard links and CLAUDE.local.md @imports.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
R="$HOME/.termalator-spike"; P="$R/projects/demo"; WT="$R/worktrees/demo-t1"
TAIL=" Use only that one tool call. Then reply with one line: OK plus the result, or DENIED plus the exact error text."
"$HERE/setup.sh" >/dev/null

echo "## hard links"
mkdir -p "$WT/.termalator/hard"
ln "$P/CONTEXT.md" "$WT/.termalator/hard/CONTEXT.md"
ln "$P/TASKS.md" "$WT/.termalator/hard/TASKS.md"
"$HERE/probe.sh" alt H1-read-hardlink "Use the Read tool on .termalator/hard/CONTEXT.md and report the SECRET-CONTEXT-TOKEN.$TAIL" --permission-mode manual
"$HERE/probe.sh" alt H2-edit-hardlink "Use the Read tool on .termalator/hard/TASKS.md, then the Edit tool to replace 'T1 spike' with 'T1 spike edited'. Use only those two tools. Then reply OK or DENIED plus the exact error text." --permission-mode acceptEdits
echo "after Claude Edit: project TASKS.md = $(tail -1 "$P/TASKS.md"); same inode: $([ "$(stat -f %i "$P/TASKS.md")" = "$(stat -f %i "$WT/.termalator/hard/TASKS.md")" ] && echo yes || echo NO)"
# A writer that uses temp-file + rename (tsk's discipline) breaks the link.
printf '# Context\nSECRET-CONTEXT-TOKEN: cherry-8\n' > "$P/CONTEXT.md.tmp" && mv "$P/CONTEXT.md.tmp" "$P/CONTEXT.md"
echo "after atomic rename in project: worktree copy = $(tail -1 "$WT/.termalator/hard/CONTEXT.md")"
printf 'cross-device hard link to /tmp: '; ln "$P/MEMORY.md" /private/tmp/tm-spike-hl 2>&1 && echo ok && rm -f /private/tmp/tm-spike-hl
printf 'hard link to a directory: '; ln "$P/memory" "$WT/.termalator/hard/memory" 2>&1

echo; echo "## CLAUDE.local.md @import of absolute paths"
"$HERE/setup.sh" >/dev/null
printf '# Termalator thread\n@%s/CONTEXT.md\n@%s/threads/t1/brief.md\n' "$P" "$P" > "$WT/CLAUDE.local.md"
"$HERE/probe.sh" alt I1-import-abs "Without using any tool, tell me the SECRET-CONTEXT-TOKEN and the BRIEF-TOKEN if they appear in your context. Reply with one line." --permission-mode manual
printf '# Termalator thread\n@.termalator/project/CONTEXT.md\n' > "$WT/CLAUDE.local.md"
"$HERE/probe.sh" alt I2-import-via-symlink "Without using any tool, tell me the SECRET-CONTEXT-TOKEN if it appears in your context. Reply with one line." --permission-mode manual
git -C "$WT" status --short
