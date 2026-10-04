#!/usr/bin/env bash
# Q2: Esc during a foreground tool; foreground subagent + Esc; background
# subagent outliving the main turn; AskUserQuestion.
source "$(dirname "$0")/lib.sh"
lab_init subag; std_start $(uuidgen | tr A-Z a-z) --allowedTools Bash Agent Task
type_line "Run this exact bash command in the foreground: for i in \$(seq 1 30); do echo tick \$i; sleep 1; done"
wait_event PreToolUse 1 60; sleep 4; snap fg-running
keys Escape; sleep 4; snap after-esc-fg
echo MARK-esc-fg >> $LAB/marks
type_line "Use the Agent tool (general-purpose subagent, foreground) to count the lines in README.md with bash and report back. Then say SUB-DONE."
wait_event SubagentStart 1 60; sleep 2; snap sub-running
wait_screen 'SUB-DONE' 90; sleep 2
type_line "Launch a general-purpose Agent with run_in_background=true whose task is: run 'for i in \$(seq 1 15); do sleep 1; done; echo done' in bash and report. End your turn immediately after launching it."
wait_event Stop 3 90; snap after-launch-bg
sleep 40; snap after-bg
type_line "Use the AskUserQuestion tool to ask me whether I prefer red or blue."
wait_screen 'red|Red' 60; sleep 3; snap ask-open
keys Enter; sleep 6; snap after-answer
events
cleanup
