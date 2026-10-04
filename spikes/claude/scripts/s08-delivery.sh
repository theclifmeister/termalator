#!/usr/bin/env bash
# Q3: hook delivery cost and failure behaviour (daemon up / down / stalled).
source "$(dirname "$0")/lib.sh"
lab_init delivery
PAY='{"session_id":"x","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}'
bench() { # bench <label> <n>
  local n=$2 s e
  s=$(python3 -c 'import time;print(time.time_ns())')
  for ((i=0;i<n;i++)); do printf '%s' "$PAY" | TERMALATOR_PANE_ID=p TERMALATOR_SOCK=$SOCK "$BIN/tmhook" PreToolUse; done
  e=$(python3 -c 'import time;print(time.time_ns())')
  echo "$1: $(( (e - s) / n / 1000 )) us per hook process (n=$n), exit=$?"
}
daemon_start
bench "daemon up" 200
echo "  delivered: $(wc -l < $EVENTS)"
daemon_stop
bench "daemon down (socket file removed)" 200
"$BIN/tmd" -sock "$SOCK" -log "$EVENTS" & P=$!; sleep 0.2; kill -9 $P; wait $P 2>/dev/null
bench "daemon crashed (stale socket file)" 200
"$BIN/tmd" -sock "$SOCK" -log "$EVENTS" & P=$!; sleep 0.2; kill -STOP $P; : > $EVENTS
bench "daemon stalled (SIGSTOP, buffer fills)" 400
kill -CONT $P; sleep 3; echo "  delivered after SIGCONT: $(wc -l < $EVENTS) of 400"; kill $P
# compare with a shell + python sender like herdr's
bench_py() {
  local n=$1 s e; s=$(python3 -c 'import time;print(time.time_ns())')
  for ((i=0;i<n;i++)); do printf '%s' "$PAY" | python3 -c 'import sys,socket,json;d=sys.stdin.read();s=socket.socket(socket.AF_UNIX,socket.SOCK_DGRAM)
try: s.sendto(d.encode(),"/tmp/nonexistent.sock")
except Exception: pass'; done
  e=$(python3 -c 'import time;print(time.time_ns())'); echo "python3 sender baseline: $(( (e - s) / n / 1000 )) us"
}
bench_py 50
echo "--- payload size limits (stream transport, full payloads)"
lab_init big; daemon_start
for n in 1000 2000 3000 9000 100000; do
  python3 -c "import json;print(json.dumps({'hook_event_name':'PreToolUse','tool_input':{'content':'x'*$n}}))" |
    TERMALATOR_FULL_PAYLOAD=1 TERMALATOR_SOCK=$SOCK "$BIN/tmhook" PreToolUse
done
sleep 0.3; echo "delivered $(wc -l < $EVENTS) of 5"; daemon_stop
echo "--- same with TERMALATOR_TRANSPORT=dgram"
: > $EVENTS; "$BIN/tmd" -dgram -sock "$SOCK" -log "$EVENTS" & P=$!; sleep 0.2
for n in 1000 2000 3000 9000 100000; do
  python3 -c "import json;print(json.dumps({'hook_event_name':'PreToolUse','tool_input':{'content':'x'*$n}}))" |
    TERMALATOR_TRANSPORT=dgram TERMALATOR_FULL_PAYLOAD=1 TERMALATOR_SOCK=$SOCK "$BIN/tmhook" PreToolUse
done
sleep 0.3; echo "delivered $(wc -l < $EVENTS) of 5"; kill $P
