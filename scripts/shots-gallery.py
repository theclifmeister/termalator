#!/usr/bin/env python3
"""Render tm's captured frames (<size>-<nn>-<name>.ansi) as one HTML gallery.

usage: scripts/shots-gallery.py SHOTS_DIR OUT.html [TITLE]
Each frame is replayed onto a cell grid (CUP, SGR, EL, ECH) and drawn as
spans; the page switches between a dark and a light terminal palette.
"""
import html
import os
import re
import sys
from collections import OrderedDict

SEQ = re.compile(r"\x1b\[([?0-9;:]*)([A-Za-z])")


def replay(data, cols, rows):
    grid = [[(" ", ()) for _ in range(cols)] for _ in range(rows)]
    y = x = 0
    st = ()
    i = 0
    while i < len(data):
        if data.startswith("\x1b]", i):
            j = min([k for k in (data.find("\x07", i), data.find("\x1b\\", i)) if k >= 0] or [len(data)])
            i = j + (1 if data.startswith("\x07", j) else 2)
            continue
        if data[i] == "\x1b":
            m = SEQ.match(data, i)
            if not m:
                i += 1
                continue
            args, cmd = m.group(1), m.group(2)
            i = m.end()
            if args.startswith("?"):
                continue
            if cmd == "H":
                p = (args.split(";") + ["1", "1"])[:2]
                y, x = int(p[0] or 1) - 1, int(p[1] or 1) - 1
            elif cmd == "m":
                st = parse_sgr(args)
            elif cmd == "K":
                if 0 <= y < rows:
                    for c in range(x, cols):
                        grid[y][c] = (" ", ())
            elif cmd == "X":
                n = int(args or 1)
                if 0 <= y < rows:
                    for c in range(x, min(cols, x + n)):
                        grid[y][c] = (" ", ())
            elif cmd == "J":
                grid = [[(" ", ()) for _ in range(cols)] for _ in range(rows)]
            continue
        ch = data[i]
        i += 1
        if ch in "\r\n":
            continue
        if 0 <= y < rows and 0 <= x < cols:
            grid[y][x] = (ch, st)
        x += 1
    return grid


def parse_sgr(args):
    out = set()
    fg = bg = None
    ps = [p for p in args.split(";")] if args else ["0"]
    j = 0
    while j < len(ps):
        p = ps[j].split(":")[0]
        n = int(p) if p.isdigit() else 0
        if n == 0:
            out, fg, bg = set(), None, None
        elif n == 1:
            out.add("b")
        elif n == 2:
            out.add("f")
        elif n == 3:
            out.add("i")
        elif n == 4:
            out.add("u")
        elif n == 7:
            out.add("r")
        elif n == 9:
            out.add("s")
        elif 30 <= n <= 37:
            fg = n - 30
        elif 90 <= n <= 97:
            fg = n - 90 + 8
        elif 40 <= n <= 47:
            bg = n - 40
        elif 100 <= n <= 107:
            bg = n - 100 + 8
        elif n in (38, 48) and j + 2 < len(ps) and ps[j + 1] == "5":
            v = int(ps[j + 2])
            if n == 38:
                fg = v
            else:
                bg = v
            j += 2
        j += 1
    return tuple(sorted(out)) + (("fg", fg), ("bg", bg))


def classes(st):
    if not st:
        return ""
    cls = []
    fg = bg = None
    for a in st:
        if isinstance(a, tuple):
            if a[0] == "fg":
                fg = a[1]
            else:
                bg = a[1]
        else:
            cls.append(a)
    if "r" in cls:
        cls.remove("r")
        fg, bg = (bg if bg is not None else "bg"), (fg if fg is not None else "fg")
    if fg is not None:
        cls.append("f%s" % fg)
    if bg is not None:
        cls.append("b%s" % bg)
    return " ".join(cls)


def render(grid):
    out = []
    for row in grid:
        line = []
        cur, buf = None, []
        for ch, st in row:
            c = classes(st)
            if c != cur:
                if buf:
                    line.append(span(cur, "".join(buf)))
                cur, buf = c, []
            buf.append(ch)
        if buf:
            line.append(span(cur, "".join(buf)))
        out.append("".join(line))
    return "\n".join(out)


def span(c, text):
    t = html.escape(text)
    return '<span class="%s">%s</span>' % (c, t) if c else t


DARK = ["#1d1f21", "#cc6666", "#b5bd68", "#f0c674", "#81a2be", "#b294bb", "#8abeb7", "#c5c8c6",
        "#666666", "#d54e53", "#b9ca4a", "#e7c547", "#7aa6da", "#c397d8", "#70c0b1", "#eaeaea"]
LIGHT = ["#000000", "#c4313b", "#1a7f37", "#9a6700", "#0550ae", "#8250df", "#1b7c83", "#6e7781",
         "#57606a", "#a40e26", "#2da44e", "#bf8700", "#0969da", "#a475f9", "#3192aa", "#8c959f"]

CSS = """
:root{--bg:#1d1f21;--fg:#c5c8c6;--page:#111;--ink:#ddd;--mut:#888;%(dark)s}
:root[data-theme=light]{--bg:#ffffff;--fg:#24292f;--page:#eef0f2;--ink:#222;--mut:#666;%(light)s}
body{background:var(--page);color:var(--ink);font:14px/1.4 -apple-system,system-ui,sans-serif;margin:0;padding:16px}
h1{font-size:20px;margin:0 0 4px} h2{font-size:16px;margin:28px 0 8px;border-bottom:1px solid var(--mut)}
.bar{position:sticky;top:0;background:var(--page);padding:8px 0;z-index:2;display:flex;gap:12px;flex-wrap:wrap;align-items:center}
button{font:inherit;padding:4px 10px;border-radius:6px;border:1px solid var(--mut);background:transparent;color:var(--ink);cursor:pointer}
.shots{display:flex;gap:16px;flex-wrap:wrap;align-items:flex-start}
figure{margin:0} figcaption{color:var(--mut);font-size:12px;margin-bottom:4px}
pre.t{background:var(--bg);color:var(--fg);font:12px/1.18 "SF Mono",Menlo,"DejaVu Sans Mono",monospace;margin:0;padding:6px;border-radius:6px;overflow:auto;max-width:calc(100vw - 48px)}
.b{font-weight:bold}.f{opacity:.55}.i{font-style:italic}.u{text-decoration:underline}.s{text-decoration:line-through}
%(cls)s
.ffg{color:var(--fg)}.fbg{color:var(--bg)}.bbg{background:var(--bg)}.bfg{background:var(--fg)}
nav a{color:var(--ink);margin-right:8px;font-size:12px}
"""


def css():
    dark = "".join("--c%d:%s;" % (i, c) for i, c in enumerate(DARK))
    light = "".join("--c%d:%s;" % (i, c) for i, c in enumerate(LIGHT))
    cls = "".join(".f%d{color:var(--c%d)}.b%d{background:var(--c%d)}" % (i, i, i, i) for i in range(16))
    return CSS % {"dark": dark, "light": light, "cls": cls}


SIZES = {"narrow": (80, 24), "normal": (120, 36), "wide": (200, 50)}


def main():
    src, dst = sys.argv[1], sys.argv[2]
    title = sys.argv[3] if len(sys.argv) > 3 else "tm screens"
    groups = OrderedDict()
    names = sorted(f for f in os.listdir(src) if f.endswith(".ansi"))
    order = {}
    for f in names:
        m = re.match(r"(narrow|normal|wide)-(\d+)-(.+)\.ansi$", f)
        if not m:
            continue
        size, n, name = m.groups()
        order.setdefault(name, (int(n), name))
        groups.setdefault(name, {})[size] = f
    parts = ['<!doctype html><meta charset=utf-8><title>%s</title><style>%s</style>' % (html.escape(title), css())]
    parts.append('<h1>%s</h1><div class=bar><button onclick="t()">dark / light terminal</button>'
                 '<label><input type=checkbox id=w onchange="document.querySelectorAll(\'.wide\').forEach(e=>e.hidden=!this.checked)" checked> wide</label>'
                 '<label><input type=checkbox onchange="document.querySelectorAll(\'.narrow\').forEach(e=>e.hidden=!this.checked)" checked> narrow</label>'
                 '<label><input type=checkbox onchange="document.querySelectorAll(\'.normal\').forEach(e=>e.hidden=!this.checked)" checked> normal</label></div>'
                 % html.escape(title))
    keys = sorted(groups, key=lambda k: order[k])
    only = os.environ.get("ONLY")
    if only:
        keys = [k for k in keys if re.fullmatch(only, k)]
    if os.environ.get("BARE"):
        parts = [parts[0] + "<style>body{padding:4px}h1,.bar,nav,h2,figcaption{display:none}</style>"]
    parts.append("<nav>" + "".join('<a href="#%s">%s</a>' % (k, k) for k in keys) + "</nav>")
    for k in keys:
        parts.append('<h2 id="%s">%s</h2><div class=shots>' % (k, k))
        for size in os.environ.get("SIZES", "narrow normal wide").split():
            f = groups[k].get(size)
            if not f:
                continue
            cols, rows = SIZES[size]
            data = open(os.path.join(src, f), encoding="utf-8", errors="replace").read()
            parts.append('<figure class="%s"><figcaption>%s · %d×%d</figcaption><pre class=t>%s</pre></figure>'
                         % (size, size, cols, rows, render(replay(data, cols, rows))))
        parts.append("</div>")
    parts.append("<script>function t(){var r=document.documentElement;r.dataset.theme=r.dataset.theme=='light'?'':'light'}</script>")
    open(dst, "w", encoding="utf-8").write("\n".join(parts))


if __name__ == "__main__":
    main()
