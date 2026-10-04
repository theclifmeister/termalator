#!/usr/bin/env bash
# Q5: headless stream-json event types; trust dialog in a new worktree of a
# trusted repo.
source "$(dirname "$0")/lib.sh"
lab_init headless
cd $LAB/repo
echo "--- stream-json message types (with --include-hook-events)"
claude -p --model $MODEL --output-format stream-json --verbose --include-hook-events \
  --plugin-dir $SPIKE/plugin/termalator "Reply HEADLESS-OK" 2>&1 | python3 -c "
import json,sys,collections
c=collections.Counter()
for l in sys.stdin:
  try: d=json.loads(l)
  except Exception: continue
  k=d.get('type')+('/'+d['subtype'] if d.get('subtype') else '')
  if d.get('type')=='system' and 'hook' in d.get('subtype',''): k+=':'+str(d.get('hook_event') or d.get('hook_name'))
  c[k]+=1
for k,v in c.items(): print('  ',k,v)"
echo "--- trust: first launch in the main repo, then a fresh worktree"
claude_start $(uuidgen | tr A-Z a-z); trust; wait_screen '^❯' 30 >/dev/null && echo "main repo trusted"; type_line /exit; sleep 2
git -C $LAB/repo worktree add -q $LAB/wt -b wt-test
CWD=$LAB/wt claude_start $(uuidgen | tr A-Z a-z); sleep 5
screen | grep -q 'trust this folder' && echo "worktree: TRUST DIALOG shown" || echo "worktree: no trust dialog"
cleanup
