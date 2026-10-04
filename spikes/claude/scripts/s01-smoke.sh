#!/usr/bin/env bash
# Smoke: plugin hooks fire, SessionStart additionalContext reaches the model.
source "$(dirname "$0")/lib.sh"
lab_init smoke; echo "CTX-MARKER-42: you are thread smoke." > $LAB/context.md; daemon_start
claude_start $(uuidgen | tr A-Z a-z) --plugin-dir $SPIKE/plugin/termalator --debug-file $LAB/debug.log
trust; wait_screen '❯' 30; snap ready; sleep 1
type_line "Reply with just the word PONG and the CTX marker number if you know one."
wait_event Stop 1 60; sleep 1; snap after
events; echo; tail -25 $LAB/screens.txt; cat $LAB/timing.txt
cleanup
