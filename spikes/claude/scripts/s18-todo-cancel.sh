#!/usr/bin/env bash
# Extra: Esc mid task list: what do hooks and ~/.claude/tasks say afterwards?
source "$(dirname "$0")/lib.sh"
lab_init todo3; SID=$(uuidgen | tr A-Z a-z); ME=$SID
std_start $SID --allowedTools Bash
type_line "Create tasks k1, k2 with your task list tool. Then for each: mark it in progress, run this bash command in the foreground: for i in \$(seq 1 20); do echo \$i; sleep 1; done ; then mark it completed."
wait_event PreToolUse 4 90; sleep 0.5
until grep -q '"tool_name":"Bash"' $EVENTS; do sleep 0.3; done; sleep 3
keys Escape; sleep 4
echo "--- hook mirror (last status per task from TaskUpdate tool_input):"
python3 - $EVENTS <<'PY'
import json,sys
st={}
for l in open(sys.argv[1]):
    p=json.loads(l).get('payload') or {}
    if isinstance(p,dict) and p.get('hook_event_name')=='PostToolUse':
        ti=p.get('tool_input') or {}
        if p.get('tool_name')=='TaskCreate': st[p['tool_response']['task']['id']]=(ti.get('subject'),'pending')
        if p.get('tool_name')=='TaskUpdate' and 'status' in ti: st[ti['taskId']]=(st.get(ti['taskId'],('?',))[0],ti['status'])
print(st)
PY
echo "--- files:"; for f in ~/.claude/tasks/$ME/*.json; do python3 -c "import json;d=json.load(open('$f'));print(d['id'],d['subject'],d['status'])"; done
echo "--- events tail:"; events | tail -6
cleanup
