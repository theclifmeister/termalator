#!/usr/bin/env bash
# Q5: --resume <id> keeps the session id? --continue? --session-id reuse?
source "$(dirname "$0")/lib.sh"
lab_init resume2
SID=$(uuidgen | tr A-Z a-z)
std_start $SID
type_line "Remember the word PERIWINKLE (no tools, no memory files). Reply OK."
wait_event Stop 1 60; type_line "/exit"; sleep 3
step() { # step <label> <claude args...>
  local l=$1; shift
  echo "--- $l"
  claude_start "" "$@" --plugin-dir "$SPIKE/plugin/termalator"
  sleep 6; snap "$l"; screen | grep -v '^$' | grep -v 'Model:\|cwd:\|manual mode\|──' | tail -4
  if screen | grep -q '^❯'; then
    local n=$(grep -c '"Stop"' $EVENTS)
    type_line "Which word did I ask you to remember? One word."
    wait_event Stop $((n+1)) 60 && grep '"Stop"' $EVENTS | tail -1 | python3 -c "import json,sys;p=json.load(sys.stdin)['payload'];print('  answer:',p['last_assistant_message'],' session:',p['session_id'])"
  fi
  type_line "/exit"; sleep 3
}
step resume-id --resume $SID
step continue --continue
step resume-fork --resume $SID --fork-session
echo "--- --session-id reuse of an existing id"
claude_start $SID; sleep 5; screen | grep -v '^$' | head -4; echo "dead=$(pane_dead)"
echo "launched as $SID"
events | grep -E 'SessionStart|SessionEnd'
cleanup
