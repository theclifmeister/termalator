#!/usr/bin/env bash
# Q1: do hooks from --plugin-dir / --settings / project .claude/settings*.json
# add to the user's own ~/.claude hooks, or replace them?
# The user's existing orca hook is the probe: with ORCA_* env set it POSTs to
# tmd's http listener (tag user-orca). Our own hooks tag themselves via argv.
source "$(dirname "$0")/lib.sh"
PORT=48123
EXTRA_ENV="ORCA_AGENT_HOOK_PORT=$PORT ORCA_AGENT_HOOK_TOKEN=x ORCA_PANE_KEY=probe"
mk_hooks() { # mk_hooks <tag> -> settings JSON with hooks for 3 events
  python3 -c "
import json,sys
t=sys.argv[1]
print(json.dumps({'hooks':{e:[{'hooks':[{'type':'command','command':'\"\$TERMALATOR_HOOK_BIN\" '+t+':'+e}]}] for e in ['SessionStart','UserPromptSubmit','Stop']}}))" "$1"
}
run() { # run <name> [claude args]
  local n=$1; shift
  lab_init "merge-$n"; daemon_start -http 127.0.0.1:$PORT
  [ -n "$PROJECT" ] && { mkdir -p $LAB/repo/.claude; mk_hooks project > $LAB/repo/.claude/settings.json; mk_hooks local > $LAB/repo/.claude/settings.local.json; }
  mk_hooks settings > $LAB/settings.json
  claude_start $(uuidgen | tr A-Z a-z) "${@//@LAB@/$LAB}"
  trust; wait_screen '^❯' 30 >/dev/null
  type_line "Reply OK"; wait_event Stop 1 60; sleep 1.5
  echo "== $n: $*"
  python3 - "$EVENTS" <<'PY'
import json,sys,collections
c=collections.Counter()
for l in open(sys.argv[1]):
  r=json.loads(l); p=r.get('payload') or {}
  if isinstance(p,dict) and p.get('hook_event_name'):
    tag=r.get('argv_ev','')
    tag=tag.split(':')[0] if ':' in tag else ('plugin' if r['via']=='sock' else tag)
    c[(tag,p['hook_event_name'])]+=1
for k in sorted(c): print('  ',k[0].ljust(10),k[1].ljust(18),c[k])
PY
  cleanup
}
P=$SPIKE/plugin/termalator
run plugin --plugin-dir $P
run settings --settings @LAB@/settings.json
run settings-inline --settings "$(mk_hooks inline)"
PROJECT=1 run all --plugin-dir $P --settings @LAB@/settings.json
PROJECT=1 run sources-project --setting-sources project,local --plugin-dir $P --settings @LAB@/settings.json
