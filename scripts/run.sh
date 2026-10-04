#!/bin/sh
# Try termalator in one command (`make run`): start the background server,
# start a session in it, and show that session.
#
#   scripts/run.sh            a session running your shell
#   scripts/run.sh top        a session running `top`
#
# Milestones change two steps here:
#   - START (M3): start an agent session, e.g. `tm session start --agent claude`.
#   - SHOW  (M2, done): attach to the session; Ctrl+\ detaches.
set -eu
TM=${TM:-tm}

"$TM" server start

# START: the session. Without arguments it runs your login shell.
if [ $# -gt 0 ]; then
	id=$("$TM" session start -- "$@")
else
	id=$("$TM" session start)
fi
echo "started session $id"

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

# SHOW: attach to the session in this terminal. Without a terminal (CI,
# a pipe), print the server's view of the screen instead.
if [ -t 0 ] && [ -t 1 ]; then
	echo "attaching to $id; detach with Ctrl+\\"
	"$TM" attach "$id"
else
	echo "──── tm session read $id ────"
	"$TM" session read "$id"
	echo "─────────────────────────────"
fi
cat <<HINT
$id keeps running in the background server, even if you close this terminal.
  $TM attach $id                         attach again (Ctrl+\\ detaches)
  $TM session list                       list sessions
  $TM session keys $id --enter 'ls'     type into it
  $TM session read $id                   show its screen as text
  $TM session stop $id                   stop it
  $TM server stop                        stop the server and every session
HINT
