#!/usr/bin/env bash
# Q4: positional kickoff prompt + --append-system-prompt-file, in an untrusted
# dir (trust dialog first) and in an already-trusted dir.
source "$(dirname "$0")/lib.sh"
kick() { # kick <name>
  lab_init "$1"
  printf 'Your brief codeword is ZEPHYR-11.\n' > $LAB/brief.md
  daemon_start
  claude_start $(uuidgen | tr A-Z a-z) --plugin-dir "$SPIKE/plugin/termalator" --append-system-prompt-file $LAB/brief.md \
    "Reply with exactly: KICKOFF-RECEIVED and the brief codeword."
  trust
  if wait_event Stop 1 40; then echo "$1: $(python3 -c "import json;[print(json.loads(l)['payload'].get('last_assistant_message')) for l in open('$EVENTS') if '\"Stop\"' in l]")"; else echo "$1: kickoff NOT submitted"; screen | grep '❯'; fi
  ls -la $(dirname $(python3 -c "import json;[print(json.loads(l).get('msg_sock','')) for l in open('$EVENTS') if 'msg_sock' in l]" | head -1))/ 2>/dev/null | head -3
  cleanup
}
kick kick-untrusted-$RANDOM
kick inject   # path already trusted by s09
