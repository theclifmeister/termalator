#!/usr/bin/env bash
# Q4: brief/prompt injection: initial prompt arg + --append-system-prompt-file,
# bracketed paste when idle and while working, and Claude's uds-messaging socket.
source "$(dirname "$0")/lib.sh"
lab_init inject
printf 'You are thread inject. Your brief codeword is ZEPHYR-11. When asked for the brief codeword, reply with it.\n' > $LAB/brief.md
std_start $(uuidgen | tr A-Z a-z) --append-system-prompt-file $LAB/brief.md --allowedTools Bash \
  "Reply with exactly: KICKOFF-RECEIVED and the brief codeword."
wait_event Stop 1 60; snap after-kickoff
MS=$(python3 -c "import json;[print(json.loads(l).get('msg_sock','')) for l in open('$EVENTS') if 'msg_sock' in l]" | grep . | head -1)
MT=$(python3 -c "import json;[print(json.loads(l).get('msg_token','')) for l in open('$EVENTS') if 'msg_token' in l]" | grep . | head -1)
echo "messaging socket from hook env: $MS (token ${#MT} chars)"
echo "--- multi-line bracketed paste while idle"
paste $'Line one of a pasted brief.\nLine two: reply with PASTE-OK and the number of lines you received.'
wait_event Stop 2 60; snap after-paste
echo "--- paste while working (should queue)"
type_line "Run in bash: for i in 1 2 3 4 5 6; do echo t\$i; sleep 1; done ; then say LOOP-DONE"
wait_event PreToolUse 1 60; sleep 1
paste "Then reply QUEUED-OK."
sleep 1; snap queued-paste
wait_screen 'QUEUED-OK' 90; sleep 2
echo "--- uds-messaging socket while idle"
N=$(grep -c '"Stop"' $EVENTS)
{ printf '{"type":"auth","token":"%s"}\n' "$MT"; printf '{"type":"user","message":{"role":"user","content":"Message via uds socket: reply UDS-OK."}}\n'; } | nc -U -w 2 "$MS"; echo "nc exit=$?"
wait_event Stop $((N+1)) 60; snap after-uds
echo "--- uds-messaging while working"
type_line "Run in bash: for i in 1 2 3 4 5 6; do echo t\$i; sleep 1; done ; then say LOOP2-DONE"
wait_event PreToolUse 2 60; sleep 1
{ printf '{"type":"auth","token":"%s"}\n' "$MT"; printf '{"type":"user","message":{"role":"user","content":"Second uds message: reply UDS2-OK."}}\n'; } | nc -U -w 2 "$MS"
sleep 2; snap uds-while-working
wait_screen 'UDS2-OK' 90; sleep 2; snap end
echo "--- uds without token"
printf '{"type":"user","message":{"role":"user","content":"no token: reply NOTOKEN"}}\n' | nc -U -w 2 "$MS"; echo " (exit=$?)"
sleep 6; screen | grep -c NOTOKEN
events
cleanup
