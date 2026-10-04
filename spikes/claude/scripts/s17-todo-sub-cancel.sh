#!/usr/bin/env bash
# Extra: task list inside a subagent, cancel mid-list, live ~/.claude/tasks
# files, and transcript entries for the task tools.
source "$(dirname "$0")/lib.sh"
lab_init todo2; SID=$(uuidgen | tr A-Z a-z); ME=$SID
std_start $SID --allowedTools 'Bash(echo *)' Agent
type_line "Use your task list tool to create two tasks named p1 and p2 and mark p1 in progress. Then stop and say READY-ONE."
wait_event Stop 1 90; sleep 1
echo "--- live task dir for $ME:"; /bin/ls -la ~/.claude/tasks/$ME/; for f in ~/.claude/tasks/$ME/*.json; do echo "$f:"; cat "$f"; echo; done
echo "--- subagent"
type_line "Launch one general-purpose Agent and wait for its result (foreground). Its task: use your task list tool to create tasks s1 and s2, run echo for each, mark both completed, then report. After it returns, say SUBDONE-NOW."
wait_event Stop 2 240; sleep 5
echo "--- cancel mid-list"
type_line "Create tasks c1, c2, c3 with your task list tool. For each: mark in progress, run: echo working-on-it, mark completed. Go."
wait_event TaskCreated 6 90 || wait_event TaskCreated 3 30; sleep 1.5; keys Escape; sleep 4; snap after-cancel
echo "--- task dir after cancel:"; for f in ~/.claude/tasks/$ME/*.json; do python3 -c "import json;d=json.load(open('$f'));print(d.get('id'),d.get('subject'),d.get('status'))"; done
/bin/ls ~/.claude/tasks/ | wc -l
echo "--- transcript task entries"
T=$(grep -o '"transcript_path":"[^"]*"' $EVENTS | head -1 | cut -d'"' -f4)
python3 - "$T" <<'PY'
import json,sys
for l in open(sys.argv[1]):
    r=json.loads(l)
    m=r.get('message') or {}
    c=m.get('content') if isinstance(m,dict) else None
    if isinstance(c,list):
        for x in c:
            if x.get('type')=='tool_use' and 'Task' in x.get('name','') and x.get('name')!='TaskOutput':
                print('tool_use', x['name'], json.dumps(x.get('input'))[:150])
    if r.get('type')=='attachment' and 'task' in json.dumps(r.get('attachment',{}))[:200].lower():
        print('attachment', json.dumps(r.get('attachment'))[:300])
PY
echo "--- subagent transcripts"
ls $(dirname $T)/$ME/subagents/ 2>/dev/null | head
cleanup
echo "--- task dir after exit:"; /bin/ls -la ~/.claude/tasks/$ME/
