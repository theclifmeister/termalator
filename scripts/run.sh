#!/bin/sh
# Try termalator in one command (`make run`): start the background server,
# start a session in it, and show that session.
#
#   scripts/run.sh            a Claude Code session in the current directory
#                             (your shell if claude isn't installed)
#   scripts/run.sh top        a session running `top`
#
# Milestones change two steps here:
#   - START (M3): start an agent session with `tm session start --agent claude`.
#   - SHOW  (M2): replace the screen dump with `exec "$TM" attach "$id"`.
set -eu
TM=${TM:-tm}

"$TM" server start

# START: the session. Without arguments it starts Claude Code, or your
# login shell when claude isn't on PATH.
agent=
if [ $# -gt 0 ]; then
	id=$("$TM" session start -- "$@")
elif command -v claude >/dev/null 2>&1; then
	id=$("$TM" session start --agent claude)
	agent=claude
else
	echo "claude is not on PATH; starting your shell instead"
	id=$("$TM" session start)
fi
echo "started session $id"

if [ -n "$agent" ]; then
	# The agent's state: idle once it's ready, blocked/trust if Claude asks
	# whether to trust this folder.
	state=$("$TM" session wait "$id" --state idle,blocked --timeout 30s || true)
	echo "$agent is $state"
else
	# Wait for the program to draw something (a shell prompt, say).
	i=0
	while [ $i -lt 50 ] && [ -z "$("$TM" session read "$id" | tr -d '[:space:]')" ]; do
		sleep 0.1
		i=$((i + 1))
	done
	if [ $# -eq 0 ]; then
		"$TM" session keys "$id" --enter 'echo "hello from termalator session $TERMALATOR_SESSION"'
		i=0
		while [ $i -lt 50 ] && ! "$TM" session read "$id" | grep -q '^hello from termalator session'; do
			sleep 0.1
			i=$((i + 1))
		done
	fi
fi

# SHOW: until the attach client lands (M2), print the server's view of the
# screen.
echo "──── tm session read $id ────"
"$TM" session read "$id"
echo "─────────────────────────────"
cat <<HINT
$id keeps running in the background server, even if you close this terminal.
  $TM session list                       list sessions (with the agent's state)
  $TM session prompt $id 'say hi'      prompt the agent (pasted once it is idle)
  $TM agent explain $id                  which signals decided the agent's state
  $TM session keys $id --enter 'ls'     type into it
  $TM session read $id                   show its screen again
  $TM session stop $id                   stop it
  $TM server stop                        stop the server and every session
HINT
