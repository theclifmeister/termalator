#!/usr/bin/env python3
"""Render tm's golden screen captures as coloured HTML terminal frames.

The screens on terminatr.dev are real tm screens: the end-to-end tests'
golden captures (internal/e2e/testdata/golden). This fills in their masks
(<duration>, <session>, ...) with neutral values, keeps every column where
tm put it, colours the glyphs and writes each frame into site/index.html
between its <!-- screen:NAME --> and <!-- /screen:NAME --> markers.

    python3 site/tools/screens.py        # from the repository root

Run it again after a golden changes. The site itself has no build step:
the output is committed.
"""

import html
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]
GOLDEN = ROOT / "internal/e2e/testdata/golden"
INDEX = ROOT / "site/index.html"

# Masked values. A value is padded or trimmed so that the
# border after it keeps the column it has on the line above.
MASKS = {
    "<duration>": "4m",
    "<session>": "s-3",
    "<tmp>": "~/src/demo",
    "<host>": "this machine",
    "<worktree>": "…/worktrees/demo/t-0001-fix-the-login",
}

SCREENS = {
    "split": "dashboard-split.txt",
    "thread": "dashboard-thread.txt",
    "needs": "dashboard-needs-you.txt",
    "tasks": "popup-tasks.txt",
    "overview": "popup-overview.txt",
    "inbox": "dashboard-inbox.txt",
    "sidebar": "sidebar-unicode.txt",
    "coordinator": "infopanel-coordinator",
    "threadpanel": "infopanel-thread",
}

BORDERS = "│╭╮╰╯"


def border_cols(line):
    return [i for i, c in enumerate(line) if c in BORDERS]


def unmask(lines):
    out = []
    for n, line in enumerate(lines):
        if "<" not in line:
            out.append(line)
            continue
        ref = border_cols(out[-1]) if out else []
        for mask, value in MASKS.items():
            while mask in line:
                at = line.index(mask)
                after = line[at + len(mask):]
                if after.startswith("  ") and not any(c in BORDERS for c in after):
                    # A column follows and no border to align on: keep its place.
                    value = value.ljust(len(mask))
                line = line[:at] + value + line[at + len(mask):]
                # The first border after the value goes back to its column.
                nxt = [i for i, c in enumerate(line) if c in BORDERS and i > at]
                want = [c for c in ref if c > at]
                if nxt and want:
                    gap = want[0] - nxt[0]
                    end = at + len(value)
                    if gap > 0:
                        line = line[:end] + " " * gap + line[end:]
                    elif gap < 0:
                        # Take the surplus from the spaces after the value,
                        # else from the text before the border.
                        cut = -gap
                        tail = line[end:nxt[0]]
                        spaces = len(tail) - len(tail.lstrip(" "))
                        take = min(cut, max(spaces - 1, 0))
                        line = line[:end] + line[end + take:]
                        cut -= take
                        if cut:
                            b = nxt[0] - take
                            line = line[:b - cut - 1] + "…" + line[b:]
        out.append(line)
    return out


# Token colouring, applied to escaped text. Order matters: the first rule
# that matches a stretch of text wins.
RULES = [
    (r"● server ok", "ok"),
    (r"[╭╮╰╯│─└┘├┌┐]+", "dim"),
    (r"\b(PROJECTS|NEEDS YOU|IN MOTION|ON DECK|OTHER SESSIONS|THREADS|STEPS|THREAD|REPOSITORIES)\b", "head"),
    (r"tm dashboard", "title"),
    (r"◆ review", "warn"),
    (r"▲ blocked", "bad"),
    (r"⚑", "bad"),
    (r"● started", "ok"),
    (r"▷ running", "ok"),
    (r"○ (idle|ready)", "idle"),
    (r"✓", "ok"),
    (r"[◐▸]", "warn"),
    (r"▰+", "accent"),
    (r"▱+", "dim"),
    (r"■", "accent"),
    (r"PR #\d+|https://github\.com/\S+", "link"),
    (r"\bT\d+\b|\bt-\d{4}\b(?!-)", "task"),
    (r"report new|\d inbox\b|inbox 1", "warn"),
    (r"\d+%", "accent"),
    (r"(≡ menu|prefix\+[a-z]|\benter\b|\besc\b|← →)", "key"),
]
PATTERN = re.compile("|".join(f"(?P<g{i}>{p})" for i, (p, _) in enumerate(RULES)))


def colour(line):
    parts, pos = [], 0
    for m in PATTERN.finditer(line):
        parts.append(html.escape(line[pos:m.start()], quote=False))
        cls = RULES[int(m.lastgroup[1:])][1]
        parts.append(f'<span class="{cls}">{html.escape(m.group(0), quote=False)}</span>')
        pos = m.end()
    parts.append(html.escape(line[pos:], quote=False))
    return "".join(parts)


def render(name):
    raw = (GOLDEN / name).read_text().rstrip("\n").split("\n")
    lines = unmask(raw)
    leak = [l for l in lines if re.search(r"<\w+>|/Users/|/home/", l)]
    if leak:
        sys.exit(f"{name}: left unmasked: {leak!r}")
    cols = max(len(l) for l in lines)
    body = "\n".join(colour(l.rstrip()) for l in lines)
    return f'<pre class="screen" style="--cols:{cols}" data-golden="{name}">{body}</pre>'


def main():
    page = INDEX.read_text()
    for key, name in SCREENS.items():
        start, end = f"<!-- screen:{key} -->", f"<!-- /screen:{key} -->"
        if start not in page:
            continue
        a = page.index(start) + len(start)
        b = page.index(end)
        page = page[:a] + render(name) + page[b:]
    INDEX.write_text(page)


if __name__ == "__main__":
    main()
