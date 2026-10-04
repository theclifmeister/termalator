#!/usr/bin/env bash
# Runs one probe: claude -p in the worktree, user settings excluded, prompts auto-denied.
# The prompt goes in on stdin because --add-dir is variadic and would eat it.
# usage: probe.sh <mode-label> <probe-id> <prompt> [extra claude args...]
set -uo pipefail
ROOT="${SPIKE_ROOT:-$HOME/.termalator-spike}"
WT="${PROBE_CWD:-$ROOT/worktrees/demo-t1}"
OUT="${OUT_DIR:-$(cd "$(dirname "$0")" && pwd)/results}/$1"
mkdir -p "$OUT"
label=$1 id=$2 prompt=$3; shift 3
cd "$WT"
claude -p --model haiku --setting-sources project,local --permission-prompts none \
  --output-format json "$@" <<<"$prompt" > "$OUT/$id.json" 2> "$OUT/$id.err"
echo "$label $id exit=$?"
