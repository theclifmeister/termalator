#!/usr/bin/env bash
# Q2: permission prompt approve vs deny (Esc). Which events fire, does state clear?
source "$(dirname "$0")/lib.sh"
lab_init perm; std_start $(uuidgen | tr A-Z a-z)
type_line "Use the Write tool to create hello.txt containing hi. Do nothing else."
wait_screen 'Do you want to (create|make)' 60; sleep 1; snap permission-open
sleep 3; keys Enter   # 1. Yes
wait_event Stop 1 60; sleep 1; snap after-approve
type_line "Use the Write tool to create bye.txt containing bye. Do nothing else."
wait_screen 'Do you want to (create|make)' 60; sleep 1
keys Escape            # deny
sleep 6; snap after-deny
echo "--- Stop count after deny: $(grep -c '"Stop"' $EVENTS)"
events
cleanup
