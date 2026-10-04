#!/usr/bin/env python3
"""Prints one line per probe: result text and any permission denials."""
import json, pathlib, sys
root = pathlib.Path(__file__).parent / "results"
for d in sorted(p for p in root.iterdir() if p.is_dir()) if len(sys.argv) < 2 else [root / a for a in sys.argv[1:]]:
    print(f"=== {d.name}")
    for f in sorted(d.glob("*.json")):
        try:
            j = json.loads(f.read_text())
        except Exception as e:
            print(f"{f.stem:24} PARSE-ERROR {e}"); continue
        den = [x.get("tool_name") + ":" + json.dumps(x.get("tool_input"))[:80] for x in j.get("permission_denials", [])]
        res = (j.get("result") or "").replace("\n", " ")[:230]
        print(f"{f.stem:24} denials={den} | {res}")
    ft = d / "files.txt"
    if ft.exists(): print(ft.read_text())
