#!/usr/bin/env bash
# Q2/Q4: SessionStart source=clear/compact, additionalContext re-injection,
# Notification permission_prompt timing, SessionEnd reasons, session ids.
source "$(dirname "$0")/lib.sh"
lab_init clear
echo "PROJECT-CONTEXT: the codeword is HELIOTROPE-7." > $LAB/context.md
SID=$(uuidgen | tr A-Z a-z)
std_start $SID
type_line "What is the codeword from the project context? Answer with the codeword only."
wait_event Stop 1 60
type_line "/clear"; wait_event SessionStart 2 30; sleep 2; snap after-clear
echo "PROJECT-CONTEXT: the codeword is MARIGOLD-3 (updated after clear)." > $LAB/context.md
type_line "What is the codeword from the project context? Answer with the codeword only."
wait_event Stop 2 60
echo "PROJECT-CONTEXT: the codeword is OBSIDIAN-9 (updated before compact)." > $LAB/context.md
type_line "/compact"; wait_event SessionStart 3 120; sleep 3; snap after-compact
type_line "What is the codeword from the project context now? Answer with the codeword only."
wait_event Stop 3 60
type_line "Use the Write tool to create x.txt containing x."
wait_screen 'Do you want to' 60; sleep 15; snap perm-15s
keys Escape; sleep 2
type_line "/exit"; sleep 4
events
echo "--- session ids seen:"; grep -o '"session_id":"[^"]*"' $EVENTS | sort | uniq -c
echo "launched with $SID"
cleanup
