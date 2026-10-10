#!/usr/bin/env bash
# agent-canary.sh: the weekly agent canary (docs/SPEC.md §8.8). It runs
# the latest released Claude Code and Codex, so it is CI only: weekly.yml's
# agent-canary job installs them on a throwaway Linux runner. Never on a
# developer's machine, which has the user's logins.
#
# Non-live checks only: no login (a home of its own), and a dead proxy, so
# no request reaches a model. What tm relies on, against what tm itself
# renders (tm agent check --render):
#   - the flags and subcommands, in --help;
#   - Codex: tm's -c keys and permission profile with --strict-config
#     (exec stops at an unknown field before any network), each group
#     alone; tm's hooks, trusted by their hash, firing in exec with the
#     payload fields tm reads; the rollout's events;
#   - Claude: tm's plugin and settings in print mode, whose exec-form
#     hooks fire before it finds no login;
#   - the screen and file text tm matches, in the binaries;
#   - the models probe (docs/SPEC.md §8.2, Models): tm doctor asks each
#     agent for its models; logged out, as here, its logged-out rule must
#     match what the agent answers, and Claude on Bedrock still lists.
# One line per check, "ok" or "FAIL"; exit 1 when any failed.
#
#   CI=true TM=./tm scripts/agent-canary.sh
set -uo pipefail

[ "${CI:-}" = true ] || { echo "agent-canary.sh runs agent binaries: CI only (weekly.yml)" >&2; exit 2; }
tm=${TM:?set TM to a tm binary}
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d)
export HOME="$work/home" CODEX_HOME="$work/home/.codex"
mkdir -p "$CODEX_HOME"
export HTTPS_PROXY=http://127.0.0.1:9 HTTP_PROXY=http://127.0.0.1:9 ALL_PROXY=http://127.0.0.1:9
export https_proxy=$HTTPS_PROXY http_proxy=$HTTP_PROXY all_proxy=$ALL_PROXY
export DISABLE_AUTOUPDATER=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 NO_COLOR=1

failed=0
ok() { echo "ok    $1"; }
fail() {
	echo "FAIL  $1${2:+: $2}"
	failed=1
}
# check NAME CMD...: CMD's exit status.
check() {
	name=$1
	shift
	if "$@" >/dev/null 2>&1; then ok "$name"; else fail "$name"; fi
}
# helps NAME TEXT CMD...: CMD's output has TEXT.
helps() {
	name=$1 want=$2
	shift 2
	if "$@" 2>&1 | grep -qF -- "$want"; then ok "$name"; else fail "$name" "no \"$want\" in $*"; fi
}
# strings NAME FILE TEXT...: the binary has every TEXT. A hint, not proof:
# text can be assembled at run time, so a FAIL here is checked by hand.
strings_in() {
	name=$1 file=$2
	shift 2
	for s in "$@"; do
		if ! LC_ALL=C grep -qaF -- "$s" "$file"; then
			fail "$name" "no \"$s\" in $(basename "$file")"
			return
		fi
	done
	ok "$name"
}

# The stand-in tm the hooks call: it logs each event's payload, and
# answers SessionStart with a context, as tm does.
fake="$work/fake-tm"
cat >"$fake" <<'EOF'
#!/bin/sh
p=$(cat)
printf '%s\n' "$p" >> "$CANARY_EVENTS"
case "$p" in *'"SessionStart"'*) printf '%s' '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"TM_CANARY_CONTEXT"}}' ;; esac
EOF
chmod +x "$fake"

# argv FILE: the launch's argv, one per line (NUL-free: tm's values are
# TOML strings, with newlines escaped).
argv() { jq -r '.argv[]' "$1"; }

codex_canary() {
	echo "== codex $(codex --version 2>&1 | tail -1)"
	helps codex-flag-config "--config" codex --help
	helps codex-flag-approve-for-me "--approve-for-me" codex --help
	helps codex-flag-ask-for-approval "on-request" codex --help
	helps codex-flag-yolo "--dangerously-bypass-approvals-and-sandbox" codex --help
	helps codex-flag-model "--model" codex --help
	helps codex-exec-strict-config "--strict-config" codex exec --help
	helps codex-resume "SESSION_ID" codex resume --help
	helps codex-queue-thread "--thread" codex queue --help
	helps codex-queue-message "--message" codex queue --help

	for role in thread coordinator; do
		d="$work/codex-$role"
		"$tm" agent check --render "$d" --role "$role" --tm-bin "$fake" "$root/internal/agent/manifests/codex.toml" >"$d.json" ||
			{ fail "codex-render-$role"; continue; }
		mapfile -t a < <(argv "$d.json")
		# The -c pairs: exec takes them; the approval flags are the TUI's.
		cflags=()
		for ((i = 1; i < ${#a[@]}; i++)); do
			[ "${a[i]}" = -c ] && { cflags+=(-c "${a[i + 1]}"); i=$((i + 1)); }
		done
		strict "codex-config-$role" "${cflags[@]}"
		if [ "$role" = thread ]; then
			# Each top-level key alone, so a FAIL names it: an MCP server
			# needs its command, a profile its default_permissions.
			declare -A group=()
			keys=()
			for ((i = 0; i < ${#cflags[@]}; i += 2)); do
				k=${cflags[i + 1]%%=*}
				k=${k%%.*}
				k=${k#default_}
				[ -z "${group[$k]+x}" ] && keys+=("$k") && group[$k]=""
				group[$k]+="${i} "
			done
			for k in "${keys[@]}"; do
				one=()
				for i in ${group[$k]}; do one+=(-c "${cflags[i + 1]}"); done
				strict "codex-config-$k" "${one[@]}"
			done
			hooks=()
			for ((i = 0; i < ${#cflags[@]}; i += 2)); do
				case "${cflags[i + 1]}" in hooks=* | hooks.state=*) hooks+=(-c "${cflags[i + 1]}") ;; esac
			done
			codex_hooks "${hooks[@]}"
		fi
	done

	bin=$(find "$(npm root -g)/@openai" -type f -name codex -path '*vendor*' 2>/dev/null | head -1)
	if [ -z "$bin" ]; then
		fail codex-binary "no native codex under $(npm root -g)/@openai"
		return
	fi
	strings_in codex-screen-trust-folder "$bin" "Trust this folder" "Trust and continue"
	strings_in codex-screen-hooks-review "$bin" "Hooks need review" "Continue without trusting"
	strings_in codex-screen-update "$bin" "Update available" "Skip until next version"
	strings_in codex-screen-approval "$bin" "Would you like to run the following command" "to confirm or"
	strings_in codex-screen-question "$bin" "submit answer" "submit all" "None of the above"
	strings_in codex-screen-plan-implement "$bin" "Implement this plan?"
	strings_in codex-screen-new-dialog "$bin" "Where should the new conversation run?"
	strings_in codex-screen-working "$bin" "to interrupt"
	strings_in codex-screen-composer "$bin" "Ask Codex to do anything"
	strings_in codex-hook-deny "$bin" permissionDecision hookSpecificOutput additionalContext
	strings_in codex-rollout-usage "$bin" token_count last_token_usage cached_input_tokens model_context_window used_percent task_complete turn_aborted
	strings_in codex-env "$bin" CODEX_THREAD_ID CODEX_SANDBOX_NETWORK_DISABLED
}

# strict NAME FLAGS...: codex exec --strict-config loads FLAGS: its banner
# ("session id:") comes once the config loaded; then it is stopped.
strict() {
	name=$1
	shift
	out=$(cd "$work" && timeout 20 codex exec --strict-config --skip-git-repo-check "$@" -- probe </dev/null 2>&1)
	if grep -q '^session id:' <<<"$out"; then ok "$name"; else fail "$name" "$(grep -v '^WARNING' <<<"$out" | head -3 | tr '\n' ' ')"; fi
}

# codex_hooks FLAGS...: tm's hooks run in exec, trusted by their hash,
# with the fields tm reads, and SessionStart's context reaches the rollout.
codex_hooks() {
	export CANARY_EVENTS="$work/codex-events"
	: >"$CANARY_EVENTS"
	(cd "$work" && TERMINATR_BIN="$fake" timeout 20 codex exec --skip-git-repo-check "$@" -- probe </dev/null >/dev/null 2>&1)
	for w in '"hook_event_name":"SessionStart"' '"hook_event_name":"UserPromptSubmit"' '"session_id":"' '"transcript_path":"'; do
		grep -qF -- "$w" "$CANARY_EVENTS" || { fail codex-hooks-run "no $w in the hook events"; return; }
	done
	ok codex-hooks-run
	rollout=$(head -1 "$CANARY_EVENTS" | jq -r .transcript_path)
	for w in TM_CANARY_CONTEXT '"type":"event_msg"' '"type":"task_started"'; do
		grep -qF -- "$w" "$rollout" 2>/dev/null || { fail codex-rollout "no $w in $rollout"; return; }
	done
	ok codex-rollout
}

claude_canary() {
	echo "== claude $(claude --version 2>&1 | tail -1)"
	for f in --plugin-dir --settings --session-id --resume --dangerously-skip-permissions --model --remote-control; do
		helps "claude-flag$f" "$f" claude --help
	done
	d="$work/claude"
	"$tm" agent check --render "$d" --tm-bin "$fake" "$root/internal/agent/manifests/claude.toml" >"$d.json" ||
		{ fail claude-render; return; }
	mapfile -t a < <(argv "$d.json")
	mapfile -t env < <(jq -r '.env[]' "$d.json")
	export CANARY_EVENTS="$work/claude-events"
	: >"$CANARY_EVENTS"
	# Print mode: no trust dialog; the hooks fire before "Not logged in".
	out=$(cd "$d/wt" && env "${env[@]}" timeout 60 claude -p "${a[@]:1}" </dev/null 2>&1)
	if grep -qiE 'unknown option|error: ' <<<"$out"; then fail claude-launch "$(head -3 <<<"$out" | tr '\n' ' ')"; else ok claude-launch; fi
	missing=""
	for w in '"hook_event_name":"SessionStart"' '"hook_event_name":"UserPromptSubmit"' '"source":"startup"' '"transcript_path":"' \
		'"session_id":"00000000-0000-4000-8000-000000000000"'; do
		grep -qF -- "$w" "$CANARY_EVENTS" || { missing=$w; break; }
	done
	if [ -z "$missing" ]; then ok claude-hooks-run; else fail claude-hooks-run "no $missing in the hook events; claude said: $(head -3 <<<"$out" | tr '\n' ' ')"; fi
	if claude plugin list --json 2>/dev/null | jq -e 'type == "array"' >/dev/null; then ok claude-plugin-list-json; else fail claude-plugin-list-json; fi

	bin=$(readlink -f "$(command -v claude)")
	if [ "$(wc -c <"$bin")" -lt 10000000 ]; then
		fail claude-binary "$bin is a wrapper, not the bundle: strings not checked"
		return
	fi
	strings_in claude-flag-append-system-prompt-file "$bin" append-system-prompt-file
	for ev in SessionStart UserPromptSubmit PreToolUse PostToolUse PostToolUseFailure PreCompact \
		PermissionRequest Notification Stop StopFailure SubagentStart SubagentStop SessionEnd; do
		strings_in "claude-hook-$ev" "$bin" "$ev"
	done
	strings_in claude-notification-types "$bin" permission_prompt idle_prompt elicitation_dialog agent_needs_input
	strings_in claude-hook-output "$bin" hookSpecificOutput additionalContext permissionDecision
	strings_in claude-sandbox-sockets "$bin" allowUnixSockets
	strings_in claude-status-file "$bin" messagingSocketPath waitingFor bridgeSessionId
	strings_in claude-messaging-token "$bin" CLAUDE_CODE_MESSAGING_TOKEN
	strings_in claude-task-tools "$bin" TaskCreate TaskUpdate activeForm CLAUDE_CODE_ENABLE_TODO_TOOLS
	strings_in claude-trust-config "$bin" hasTrustDialogAccepted
	strings_in claude-transcript "$bin" "[Request interrupted by user" turn_duration
	strings_in claude-screen-trust-folder "$bin" "Yes, I trust this folder"
	strings_in claude-screen-bypass "$bin" "Yes, I accept" "Bypass Permissions"
	strings_in claude-screen-transcript-view "$bin" "detailed transcript"
	strings_in claude-screen-permission "$bin" "Do you want to "
	strings_in claude-screen-question "$bin" "Enter to select" "to navigate" "Esc to cancel" "Type something"
	strings_in claude-screen-remote-control "$bin" "Disconnect this session" "Remote Control disconnected"
}

# models NAME AGENT WANT [VAR=VALUE...]: tm doctor's "AGENT models"
# check, with the variables set, has WANT.
models() {
	name=$1 agent=$2 want=$3
	shift 3
	got=$(env "$@" TERMINATR_HOME="$work/tm-models" TERMINATR_SOCKET="$work/none.sock" "$tm" doctor --json 2>/dev/null |
		jq -r --arg n "$agent models" '.. | objects | select(.name? == $n) | .status + " " + .detail' | head -1)
	case "$got" in
	*"$want"*) ok "$name" ;;
	*) fail "$name" "doctor says \"$got\", want \"$want\"" ;;
	esac
}

models_canary() {
	models codex-models-logged-out codex "unknown: logged out of codex"
	models claude-models-logged-out claude "unknown: logged out of claude"
	models claude-models-bedrock claude "models, asked" CLAUDE_CODE_USE_BEDROCK=1 AWS_REGION=us-east-1
}

case "${1:-all}" in
codex) codex_canary ;;
claude) claude_canary ;;
models) models_canary ;;
all) codex_canary; claude_canary; models_canary ;;
*) echo "usage: agent-canary.sh [codex|claude|models|all]" >&2; exit 2 ;;
esac
exit $failed
