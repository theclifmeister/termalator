#!/usr/bin/env bash
# Q5: does `claude agents --json` expose state for interactive sessions?
source "$(dirname "$0")/lib.sh"
lab_init agentsjson
SID=$(uuidgen | tr A-Z a-z); ME=$SID
std_start $SID
aj() { echo "== $1"; claude agents --json 2>&1 | python3 -c "
import json,sys
d=json.load(sys.stdin)
for a in d:
  if '$ME' in json.dumps(a): print(json.dumps(a)[:900])
print('total entries', len(d))"; }
aj idle
type_line "Run in bash: for i in 1 2 3 4 5 6 7 8; do echo t\$i; sleep 1; done"
wait_screen 'Do you want to proceed' 60; aj blocked
keys Enter; sleep 2; aj working
wait_event Stop 1 60; sleep 1; aj idle-after
cleanup
