#!/usr/bin/env bash
# Extra: which events fire when Claude updates its todo/task list, payload
# shape and size, subagents, cancel, /clear, compaction, transcript fallback.
source "$(dirname "$0")/lib.sh"
lab_init todo; std_start $(uuidgen | tr A-Z a-z) --allowedTools 'Bash(echo *)' 'Bash(sleep *)' Agent
mark() { echo "{\"ts_ns\":$(python3 -c 'import time;print(time.time_ns())'),\"screen\":\"----\",\"why\":\"MARK $1\",\"sfile\":\"\"}" >> $LAB/screen.jsonl; }
mark "1 main agent todo list"
type_line "Use your todo/task list tool to plan exactly 3 steps named alpha, beta, gamma. Then do them one at a time (each step: run echo step-NAME in bash), marking each in progress and then completed as you go. Then say TODO-DONE."
wait_screen 'TODO-DONE' 120; wait_event Stop 1 30; sleep 2; snap after-main
mark "2 subagent todo list"
type_line "Launch one general-purpose Agent in the foreground (do not background it) whose task is: use your todo/task list tool to plan 2 steps named one and two, run echo for each, marking them completed. Wait for it and then say SUB-TODO-DONE."
wait_screen 'SUB-TODO-DONE' 180; sleep 3
mark "3 cancel mid-list"
type_line "Use your todo/task list tool to plan 3 steps named x1, x2, x3; each step is: run the bash command 'for i in 1 2 3 4 5 6 7 8; do echo \$i; done; sleep 0' then mark it completed. Go."
wait_event PreToolUse 1 60; sleep 6; keys Escape; sleep 3; snap after-cancel
keys C-u; sleep 0.5
mark "4 clear"
type_line "/clear"; sleep 4
type_line "Use your todo/task list tool to add one item named after-clear and mark it in progress. Then stop."
sleep 15
mark "5 compact"
type_line "/compact"; wait_screen 'Compacted|compacted' 120; sleep 3
type_line "Mark the after-clear item completed in your todo/task list. Then stop."
sleep 15; snap end
cleanup
