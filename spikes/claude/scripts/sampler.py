#!/usr/bin/env python3
"""Sample a tmux pane every 200ms, classify it with herdr-style screen rules,
and append a JSON line to the output each time the classification changes.

usage: sampler.py <tmux-socket-name> <session> <out.jsonl>
"""
import json, re, subprocess, sys, time

L, S, OUT = sys.argv[1:4]
SPIN_TITLE = re.compile(r"^[⠀-⣿◐-◓] ")
SPIN_LINE = re.compile(r"^\s*[*·✢✳✶✻✽]\s+\S.*…")
HRULE = re.compile(r"^\s*─{20,}")


def tmux(*a):
    return subprocess.run(["tmux", "-L", L, *a], capture_output=True, text=True).stdout


def classify(lines, title):
    # region: after the last horizontal rule that has non-empty content below
    rules = [i for i, l in enumerate(lines) if HRULE.match(l)]
    body = "\n".join(lines)
    tail = "\n".join(lines[-14:])
    lower = body.lower()
    if "showing detailed transcript" in lower:
        return "unknown", "transcript"
    # blockers anywhere in the visible screen bottom (dialogs replace the prompt box)
    if "esc to cancel" in tail.lower() and re.search(r"enter to (confirm|select)|to navigate", tail.lower()):
        return "blocked", "esc to cancel + enter to confirm/select"
    if "do you want to" in lower and re.search(r"❯?\s*1\.\s*yes", lower):
        return "blocked", "do you want to proceed + 1. Yes"
    if SPIN_TITLE.match(title):
        return "working", "title spinner"
    if "esc to interrupt" in tail.lower():
        return "working", "esc to interrupt"
    for l in lines[-14:]:
        if SPIN_LINE.match(l):
            return "working", "spinner line: " + l.strip()[:40]
    if title.startswith("✳"):
        return "idle", "title ✳"
    if any(re.match(r"^\s*❯", l) for l in lines[-12:]):
        return "idle", "prompt ❯"
    return "idle", "fallback"


def session_file(pid):
    """Claude's own state file (undocumented): ~/.claude/sessions/<pid>.json"""
    import os
    try:
        d = json.load(open(os.path.expanduser(f"~/.claude/sessions/{pid}.json")))
        st = d.get("status", "?")
        if d.get("waitingFor"):
            st += ":" + d["waitingFor"]
        return st
    except Exception:
        return "-"


last = None
with open(OUT, "a") as f:
    while True:
        dead = tmux("display", "-p", "-t", S, "#{pane_dead}").strip()
        if dead in ("", "1"):
            f.write(json.dumps({"ts_ns": time.time_ns(), "screen": "exited", "why": "pane dead"}) + "\n")
            break
        title = tmux("display", "-p", "-t", S, "#{pane_title}").strip()
        lines = tmux("capture-pane", "-p", "-t", S).rstrip("\n").split("\n")
        st, why = classify(lines, title)
        pid = tmux("display", "-p", "-t", S, "#{pane_pid}").strip()
        sf = session_file(pid)
        if (st, why.split(":")[0], sf) != last:
            last = (st, why.split(":")[0], sf)
            f.write(json.dumps({"ts_ns": time.time_ns(), "screen": st, "why": why, "title": title, "sfile": sf}) + "\n")
            f.flush()
        time.sleep(0.1)
