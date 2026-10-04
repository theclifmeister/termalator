#!/usr/bin/env bash
# SPEC open points 7, 12, 13: appended system prompt after /clear; read-only
# project folder via --settings permissions + sandbox, interactive and yolo;
# task hooks in yolo.
source "$(dirname "$0")/lib.sh"
run() { # run <name> [extra claude args]
  local n=$1; shift
  lab_init "acc-$n"
  mkdir -p $LAB/proj && echo "PROJECT NOTE: secret word is TANGERINE" > $LAB/proj/notes.md
  P=$(cd $LAB/proj && pwd -P)
  cat > $LAB/settings.json <<J
{"permissions":{"allow":["Read(/$P/**)"],"deny":["Edit(/$P/**)"]},
 "sandbox":{"enabled":true,"network":{"allowUnixSockets":["$SOCK"]}}}
J
  printf 'Your appended brief codeword is QUASAR-5.\n' > $LAB/brief.md
  std_start $(uuidgen | tr A-Z a-z) --settings $LAB/settings.json --append-system-prompt-file $LAB/brief.md "$@"
  echo "===== $n"
  ask() { # ask <label> <prompt>
    local c=$(grep -c '"Stop"' $EVENTS)
    type_line "$2"
    local i=0; while (( i < 240 )); do
      (( $(grep -c '"Stop"' $EVENTS) > c )) && break
      if screen | grep -q 'Do you want to'; then echo "  [$1] DIALOG: $(screen | grep -A3 'Do you want to' | head -4 | tr -s ' ' | tr '\n' '|')"; keys Escape; sleep 1; break; fi
      sleep 0.5; i=$((i+1))
    done
    sleep 1
    echo "  [$1] last: $(grep '"Stop"' $EVENTS | tail -1 | python3 -c "import json,sys;print(json.load(sys.stdin)['payload']['last_assistant_message'][:160].replace(chr(10),' '))" 2>/dev/null)"
  }
  ask read "Read the file $P/notes.md with the Read tool and tell me the secret word."
  ask write "Use the Write tool to create $P/new.txt containing hi. If it fails, say exactly what the error said."
  ask bash "Run this bash command exactly: echo hi > $P/bash.txt   If it fails, quote the error."
  ask task "Use your task list tool to create one task named yolo-check. Then stop."
  echo "  files in proj: $(/bin/ls $P | tr '\n' ' ')"
  echo "  TaskCreated hooks: $(grep -c '"TaskCreated"' $EVENTS)"
  type_line "/clear"; sleep 4
  ask clear "What is your appended brief codeword? Reply with only the codeword, or NONE."
  cleanup
}
[ -z "$ONLY" ] && run interactive
run yolo --dangerously-skip-permissions
