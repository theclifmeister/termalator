#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
lab_init resume3
ORIG=$(uuidgen | tr A-Z a-z)
std_start $ORIG
type_line "Remember the word PERIWINKLE (no tools). Reply OK."
wait_event Stop 1 60; type_line "/exit"; sleep 3
echo "--- fork with pre-assigned new id"
NEW=$(uuidgen | tr A-Z a-z)
claude_start $NEW --resume $ORIG --fork-session --plugin-dir "$SPIKE/plugin/termalator"
sleep 6; screen | grep -v '^$' | tail -3
grep '"SessionStart"' $EVENTS | tail -1 | python3 -c "import json,sys;p=json.load(sys.stdin)['payload'];print('  source',p['source'],'session',p['session_id'])"
echo "  orig=$ORIG new=$NEW"
type_line "/exit"; sleep 3
echo "--- --session-id reuse"
claude_start $ORIG; sleep 5; screen | grep -v '^$' | head -3; echo "dead=$(pane_dead)"
cleanup
