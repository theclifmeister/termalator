#!/usr/bin/env bash
# Q2: (a) Esc while a foreground Bash tool runs; (b) a background Bash task that
# finishes after the main turn's Stop: does Claude start a turn on its own?
source "$(dirname "$0")/lib.sh"
lab_init bgfg; std_start $(uuidgen | tr A-Z a-z) --allowedTools 'Bash(sleep *)' 'Bash(echo *)'
type_line "Run the bash command 'sleep 25' in the foreground (do NOT use run_in_background), then say finished."
wait_event PreToolUse 1 60; sleep 3; snap fg-running
keys Escape; sleep 5; snap after-esc-fg
type_line "Start 'sleep 12 && echo BG-OK' with run_in_background=true. End your turn right away. When it completes, say BG-REPORTED."
wait_event Stop 2 60 ; snap after-stop
sleep 30; snap after-bg
events
cleanup
