#!/usr/bin/env python3
"""Summarise a lab run: hook events from events.jsonl merged by time with the
screen classifier's transitions from screen.jsonl (same directory).

Columns: ms since first record, source, event, a naive hook-only state, and
the key payload fields. The hook-only state is what a reducer that trusts
hooks alone would believe, so stale cases show up as hook!=screen.
"""
import json, os, sys

path = sys.argv[1]
rows = []
for l in open(path):
    if not l.strip():
        continue
    r = json.loads(l)
    if r.get("via") == "ctx":
        rows.append((r["recv_ns"], "ctx", r))
        continue
    rows.append((r.get("sent_ns") or r["recv_ns"], "hook", r))
sp = os.path.join(os.path.dirname(path), "screen.jsonl")
if os.path.exists(sp):
    for l in open(sp):
        r = json.loads(l)
        rows.append((r["ts_ns"], "screen", r))
rows.sort(key=lambda x: x[0])


def hook_state(ev, p, cur):
    sub = bool(p.get("agent_id"))
    if ev == "SessionStart":
        return "idle"
    if ev == "UserPromptSubmit":
        return "working"
    if ev in ("PreToolUse",) and p.get("tool_name") == "AskUserQuestion":
        return "blocked"
    if ev in ("PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionDenied") and not sub:
        return "working"
    if ev == "PermissionRequest":
        return "blocked"
    if ev == "Notification":
        nt = p.get("notification_type")
        if nt in ("permission_prompt", "elicitation_dialog", "agent_needs_input"):
            return "blocked"
        if nt == "idle_prompt":
            return "idle"
    if ev in ("Stop", "StopFailure"):
        return "idle"
    if ev == "SessionEnd":
        return "exited"
    return cur


t0 = rows[0][0] if rows else 0
hs = "?"
for ts, kind, r in rows:
    ms = (ts - t0) / 1e6
    if kind == "ctx":
        print(f"{ms:8.0f}  {'ctx':<6} request {r.get('req','').strip()}")
        continue
    if kind == "screen":
        print(f"{ms:8.0f}  {'SCREEN':<6} {r['screen']:<8} sfile={r.get('sfile','-'):<26} ({r['why'][:40]})")
        continue
    p = r.get("payload") or {}
    if not isinstance(p, dict):
        p = {}
    lat = (r["recv_ns"] - r["sent_ns"]) / 1e6 if r.get("sent_ns") else 0
    ev = p.get("hook_event_name", "?")
    hs = hook_state(ev, p, hs)
    bits = []
    for k in ("source", "reason", "notification_type", "tool_name", "agent_type", "agent_id",
              "stop_reason", "message", "trigger", "prompt", "error", "is_interrupt"):
        v = p.get(k)
        if v not in (None, ""):
            s = str(v).replace("\n", " ")
            bits.append(f"{k}={s[:50]}")
    if ev in ("Stop", "SubagentStop"):
        m = (p.get("last_assistant_message") or "").replace("\n", " ")
        bits.append(f"last={m[:40]!r}")
    src = r.get("via") + ("/" + r["argv_ev"].split(":")[0] if ":" in r.get("argv_ev", "") else "")
    print(f"{ms:8.0f}  {'hook':<6} {ev:<19} hook-state={hs:<8} lat={lat:4.1f}ms {' '.join(bits)}")
