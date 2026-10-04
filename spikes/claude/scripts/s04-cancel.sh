#!/usr/bin/env bash
# Q2: Esc-cancel mid-turn, (a) during a running tool, (b) during text streaming,
# (c) queued prompt typed while working.
source "$(dirname "$0")/lib.sh"
lab_init cancel; std_start $(uuidgen | tr A-Z a-z) --allowedTools 'Bash(sleep *)'
type_line "Run the bash command: sleep 30   then say finished."
wait_event PreToolUse 1 60; sleep 3; snap tool-running
keys Escape; sleep 5; snap after-esc-tool
type_line "Write a 600 word story about a lighthouse keeper. No tools."
wait_screen 'lighthouse|Lighthouse' 60; sleep 2; snap streaming
keys Escape; sleep 5; snap after-esc-stream
echo "--- queued prompt while working"
type_line "Run the bash command: sleep 8   then say A-DONE."
wait_event PreToolUse 2 60; sleep 1
type_line "Now say B-DONE."
sleep 2; snap queued
wait_screen 'B-DONE' 60; sleep 3; snap after-queued
events
cleanup
