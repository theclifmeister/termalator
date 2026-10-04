#!/usr/bin/env bash
# Q5: resume with a pre-assigned session id, --continue, statusline as a
# signal, broken hooks (missing binary, http hook to a dead port).
source "$(dirname "$0")/lib.sh"
lab_init resume
SID=$(uuidgen | tr A-Z a-z)
cat > $LAB/settings.json <<J
{"statusLine":{"type":"command","command":"$SPIKE/scripts/statusline.sh"},
 "hooks":{"UserPromptSubmit":[{"hooks":[
   {"type":"command","command":"/nonexistent/termalator-hook"},
   {"type":"http","url":"http://127.0.0.1:1/hook","timeout":2}]}]}}
J
EXTRA_ENV="TERMALATOR_STATUS_LOG=$LAB/status.log"
std_start $SID --settings $LAB/settings.json
type_line "Remember the word PERIWINKLE. Reply OK."
wait_event Stop 1 60; sleep 1; snap broken-hooks
type_line "/exit"; sleep 3
echo "--- resume by id"
claude_start "" --resume $SID --plugin-dir "$SPIKE/plugin/termalator" 2>/dev/null
sleep 1; tmux -L $TMUX_L kill-session -t $NAME
tmux -L $TMUX_L new-session -d -s "$NAME" -x 140 -y 45 -c "$LAB/repo" env -i HOME="$HOME" USER="$USER" PATH="$BIN:$PATH" TERM=xterm-256color LANG=en_US.UTF-8 \
  TERMALATOR_PANE_ID=$NAME TERMALATOR_SOCK=$SOCK TERMALATOR_CTX_SOCK=$CTXSOCK TERMALATOR_HOOK_BIN=$BIN/tmhook TERMALATOR_FULL_PAYLOAD=1 \
  claude --model $MODEL --resume $SID --plugin-dir "$SPIKE/plugin/termalator"
wait_screen '^❯' 30 >/dev/null; sleep 1
type_line "Which word did I ask you to remember? One word."
wait_event Stop 2 60
type_line "/exit"; sleep 3
echo "--- --session-id reuse of an existing id"
tmux -L $TMUX_L kill-session -t $NAME
tmux -L $TMUX_L new-session -d -s "$NAME" -x 140 -y 45 -c "$LAB/repo" env -i HOME="$HOME" USER="$USER" PATH="$BIN:$PATH" TERM=xterm-256color \
  claude --model $MODEL --session-id $SID \; set-option remain-on-exit on
sleep 4; screen | grep -v '^$' | head -5
events
echo "--- statusline invocations: $(wc -l < $LAB/status.log)"
tail -1 $LAB/status.log | cut -d' ' -f2- | python3 -c "import json,sys;d=json.load(sys.stdin);print(sorted(d.keys()))"
cleanup
