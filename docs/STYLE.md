# tm UI style guide

Every surface tm draws follows this guide: the dashboard, the sidebar,
the details panel, the info panels, the status bar, every popup and
dialog, and the Claude mod (band, status line entry, /tm pane, toasts).
It was written for T93 and approved by the user on 2026-10-06; SPEC.md §4
says what each screen shows, this says how. The unit is one terminal
cell. Colours are the terminal's 16 ANSI colours only, so tm follows the
user's light or dark theme.

To see every screen at three sizes: `E2E=1 TM_SHOTS=/tmp/shots go test
-run TestShots ./internal/e2e`, then `scripts/shots-gallery.py /tmp/shots
shots.html` (a page with a dark and a light palette).

## Principles

- **P1. Actions live where you look.** A popup or dialog shows its own
  keys inside its frame. The footer and the status bar never carry the
  only copy of a popup's keys or of a question.
- **P2. Words first.** A glyph, colour or abbreviation comes with a word,
  or is in the help's Symbols list. No word appears in two forms.
- **P3. One look per kind of thing.** The same list, panel or dialog is
  drawn the same way wherever it appears.
- **P4. Quiet by default.** Only what needs attention gets colour or bold.

## Spacing and grid

| Token | Cells | Used for |
|---|---|---|
| gutter | 1 column | the inner margin of every region: sidebar, list, panels, popup body, menu |
| gap | 2 columns | between columns of a row (`colGap`), between a label column and its value |
| indent | 2 columns | lines under a row: steps, detail lines, wrapped items |
| section | 1 row | between sections; none between a heading and its first line |

- **S1.** Every region keeps its gutter on both sides.
- **S2.** Columns are `gap` apart and line up from row to row; the list
  sets a column's width, not each row.
- **S3.** Wrapped text hangs: continuation lines start under the text
  they continue, two cells in for a list item.
- **S4.** Key/value blocks use one faint, lowercase label column per block.
- **S5.** A selection bar spans its region's full inner width.
- **S6.** A rule inside a frame spans the full inner width.
- **S7.** Popups come in two widths: a **dialog** (questions, prompts,
  the prefix key, short lists) at most 64 columns, a **view** (help, the
  project popup, the task list, the inbox, the switcher, the settings) at
  most 96 and nine tenths of the window, both centred. A popup opened
  from another stacks over it.

## Type

| Level | Look | Case |
|---|---|---|
| Title | bold, accent | Sentence case: `tm dashboard`, `Settings`, `Tasks · demo`, a panel's task title |
| Section | bold, UPPERCASE, a faint rule to the edge (`sectionRule`) | `NEEDS YOU`, `IN MOTION`, `STEPS`, `LAST REPORT` |
| Item | plain; bold only for the item's own name | as written |
| Label | faint | lowercase |
| Value | plain; accent where it can be changed | as written |
| Meta | faint | lowercase |
| Key hint | key in accent, label faint, ` · ` faint between hints (`keysLine`) | lowercase |
| Message | plain; red for an error | lowercase or Sentence case, cleared by the next key |

- **T1.** Titles are Sentence case and name the thing: `Inbox · demo`,
  `Thread models`, `Accept T4`.
- **T2.** One spelling per word everywhere: `needs you` (`2 need you`),
  `in review`, `blocked`, `asks you`, `checks failed`, `prompts held`,
  `asked the coordinator`.
- **T3.** No abbreviations people read: inbox kinds in words (`PR
  opened`, not `pr-opened`), `×3` for repeats. Ids (`T12`, `t-0003`,
  `s-4`, `#7`) are names and stay.
- **T4.** `…` marks a cut and nothing else; a row cuts its least useful
  part first (a thread's own id is last on its row).
- **T5.** `esc close` for a view, `esc cancel` for a dialog that would
  change something, `esc back` only when it returns to the popup under it.

## Colour roles

| Role | Colour | Means |
|---|---|---|
| accent | cyan | keys, focus, the popup's border, titles, values you can change |
| good | green | working, started, done, merged, checks passed |
| warn | yellow (bold for text) | needs you, in review, attention |
| bad | red (bold for state words) | blocked, failed, refused, an error |
| info | blue | starting |
| faint | SGR 2 | labels, meta, help text, the dimmed scene under a popup |
| select | reverse | the selection where the keyboard is; faint reverse elsewhere |

- **C1.** Colour never carries meaning alone: a state has its glyph and
  its word.
- **C2.** The same thing has the same colour on every surface. Section
  headings are uncoloured, except NEEDS YOU (warn).
- **C3.** Warn text is bold and never faint (yellow is weak on light
  backgrounds).
- **C4.** Under a popup the whole scene dims, the sidebar too.
- **C5.** The status bar is one reverse row with no other colour.

## Glyphs: one meaning each

| Unicode | ASCII | Meaning |
|---|---|---|
| `●` | `*` | working; a task started |
| `○` | `o` | idle; an open or ready task |
| `▲` | `!` | blocked |
| `◆` | `#` | in review |
| `◌` | `~` | starting |
| `▷` | `:` | running (a shell or another program) |
| `✓` | `v` | done |
| `·` | `-` | stopped, exited, resolved |
| `⚑` | `?` | in the sidebar: something in this project needs you |
| `⌁` `∥` | `@` `=` | remote control on; the project is paused |
| `✓` `◐` `○` | `x` `~` `o` | a step done, under way, to do |
| `▸` | `>` | the step under way |
| `▰▱` | `#-` | progress, always beside its count |

The help's **Symbols** list (`symbolLines`) shows the set in use.

## Dialog anatomy

```
╭─ Title ──────────────────────────────────────────╮   title in the border
│ The question or the content, wrapped and hung    │   gutter each side
│ under itself.                                    │
│                                                  │   a blank row
│ y yes · n no · esc cancel                        │   the action row: the keys, as buttons
╰────────────────────────────────────── more ↓ ────╯   the scroll mark in the border
```

- **D1.** Every popup and dialog has a title in its top border.
- **D2.** Every popup and dialog ends with its action row inside the
  frame (`box.keys`, drawn by `drawBox`); each hint is a button. The
  footer under a popup shows none of them; the status bar never asks.
- **D3.** A yes/no question takes `y`, `n` and `esc` and shows
  `y yes · n no · esc cancel`; no other key answers it, `enter` included.
- **D4.** A prompt shows its label above the field and a counter only
  near its limit.
- **D5.** A message shows in the footer until the next key.
- **D6.** A menu is a dialog: its title (`Menu` for ≡), its items with
  their keys right-aligned in the accent colour, its action row.
- **D7.** A long popup shows `more ↓` / `more ↑ ↓` in its bottom border.
- In a session the same box is drawn over the panes (`amenu`): menus and
  the remote control question.

## The Claude mod

The mod draws with Claude Code's elements and colour names (`warning`,
`error`, `success`, `dimColor`), with the same words and roles as the TUI:
the TUI's state and need words, the same colour per inbox kind, headings
bold and uncoloured but Needs you, no bare figures (`steps 2/4`), toasts
in one Sentence-case sentence without a final period, buttons in
Sentence case.
