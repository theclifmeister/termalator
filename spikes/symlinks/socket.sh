#!/usr/bin/env bash
# Can a sandboxed Bash command reach a daemon on a unix socket under ~/.termalator-spike?
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SOCK="$HOME/.termalator-spike/tm.sock"
"$HERE/setup.sh" >/dev/null
rm -f "$SOCK"
python3 - "$SOCK" <<'PY' &
import socket, sys
s = socket.socket(socket.AF_UNIX); s.bind(sys.argv[1]); s.listen(5); s.settimeout(240)
try:
    while True:
        c, _ = s.accept(); c.sendall(b"PONG-from-daemon\n"); c.close()
except Exception: pass
PY
SRV=$!; sleep 1
CLIENT="python3 -c \"import socket;s=socket.socket(socket.AF_UNIX);s.connect('$SOCK');print(s.recv(64).decode())\""
TAIL=" Use only that one tool call, with the sandbox (do not request dangerouslyDisableSandbox). Then reply with one line: OK plus output, or DENIED plus the exact error text."
SB='{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true}}'
SBU="{\"sandbox\":{\"enabled\":true,\"autoAllowBashIfSandboxed\":true,\"network\":{\"allowUnixSockets\":[\"$SOCK\"]}}}"
"$HERE/probe.sh" socket S1-sandbox-default "Use the Bash tool to run: $CLIENT$TAIL" --permission-mode acceptEdits --settings "$SB" &
"$HERE/probe.sh" socket S2-sandbox-allowUnixSockets "Use the Bash tool to run: $CLIENT$TAIL" --permission-mode acceptEdits --settings "$SBU" &
wait $(jobs -p | grep -v "^$SRV$") 2>/dev/null
kill $SRV 2>/dev/null; rm -f "$SOCK"
