#!/bin/sh
# Try terminatr in one command (`make run`): start the background server
# and open the dashboard.
#
#   scripts/run.sh            the dashboard: c starts Claude Code in a
#                             directory you choose, s a shell
#   scripts/run.sh top        also start a session running `top` first
#
# Without a terminal (CI, a pipe) it lists the sessions instead.
set -eu
TM=${TM:-tm}

"$TM" server start

if [ $# -gt 0 ]; then
	id=$("$TM" session start -- "$@")
	echo "started session $id"
fi

if [ -t 0 ] && [ -t 1 ]; then
	"$TM"
else
	"$TM" session list
fi
cat <<HINT
Sessions keep running in the background server, even if you close this terminal.
  $TM                                    the dashboard again (enter attaches, Ctrl+\\ comes back)
  $TM session list                       list sessions (with the agent's state)
  $TM project new NAME                   a project; $TM project open SLUG starts its coordinator
  $TM server stop                        stop the server and every session
HINT
