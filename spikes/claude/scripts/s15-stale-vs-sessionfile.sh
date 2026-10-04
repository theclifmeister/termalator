#!/usr/bin/env bash
# Q2: replay every stale case and compare hooks, screen rules and Claude's own
# ~/.claude/sessions/<pid>.json status.
source "$(dirname "$0")/lib.sh"
lab_init stale; std_start $(uuidgen | tr A-Z a-z)
mark() { echo "{\"ts_ns\":$(python3 -c 'import time;print(time.time_ns())'),\"screen\":\"----\",\"why\":\"MARK $1\",\"sfile\":\"\"}" >> $LAB/screen.jsonl; }
mark "deny permission with Esc"
type_line "Use the Write tool to create bye.txt containing bye. Do nothing else."
wait_screen 'Do you want to (create|make)' 60; sleep 8; keys Escape; sleep 4
mark "Esc during streaming"
type_line "Write a 500 word story about a lighthouse keeper. No tools."
wait_screen 'Keeper|keeper' 60; sleep 1; keys Escape; sleep 4
mark "Esc during foreground tool (approved)"
type_line "Run this exact bash command in the foreground: for i in \$(seq 1 30); do echo tick \$i; sleep 1; done"
wait_screen 'Do you want to proceed' 60; keys Enter; sleep 4; keys Escape; sleep 4
mark "background subagent outliving the turn"
type_line "Launch a general-purpose Agent with run_in_background=true whose task is: run 'for i in \$(seq 1 12); do sleep 1; done; echo done' in bash and report. Do not wait for it; end your turn immediately."
sleep 3; screen | grep -q 'Do you want' && keys Enter
sleep 30
mark "AskUserQuestion then Esc"
type_line "Use the AskUserQuestion tool to ask me whether I prefer red or blue."
wait_screen 'Blue|blue' 60; sleep 3; keys Escape; sleep 4
events
cleanup
