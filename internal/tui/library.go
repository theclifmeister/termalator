package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/thread"
)

// The project popup's Library tab (docs/SPEC.md §4): the files every
// thread of the project attached to its reports, the resolved and
// archived threads' too, grouped by task and thread, newest first. enter
// reads one in the popup; d deletes the selected file and D the files of
// its thread, each after y. A file shows by name and size, never a path.

// libraryOrder are the library's files grouped by thread, the group with
// the newest file first and each group's files newest first.
func libraryOrder(files []thread.LibFile) []thread.LibFile {
	var ids []string
	by := map[string][]thread.LibFile{}
	for _, f := range files { // newest first already
		if _, ok := by[f.Thread]; !ok {
			ids = append(ids, f.Thread)
		}
		by[f.Thread] = append(by[f.Thread], f)
	}
	out := make([]thread.LibFile, 0, len(files))
	for _, id := range ids {
		out = append(out, by[id]...)
	}
	return out
}

// libFiles are the Library tab's files, in the order they are listed.
func (pv *projectView) libFiles() []thread.LibFile { return libraryOrder(pv.library) }

// selLib is the selected file, false for none.
func (pv *projectView) selLib() (thread.LibFile, bool) {
	fs := pv.libFiles()
	if i := pv.sel[tabLibrary]; i < len(fs) {
		return fs[i], true
	}
	return thread.LibFile{}, false
}

// libGroup is the title line of a thread's files: task, title, thread
// and the date of its newest file.
func libGroup(f thread.LibFile, w int) string {
	rest := " · " + f.Thread + " · " + f.Time.Local().Format("2006-01-02")
	if f.State == "archived" {
		rest += " · archived"
	}
	head := joinSp(f.Task, oneLine(f.Title))
	return styleHead.Render(ansi.Truncate(head, max(w-ansi.StringWidth(rest), 8), "…")) + styleFaint.Render(rest)
}

// sizeWords is a byte count in words: 812 B, 3.4 KB, 1.2 MB.
func sizeWords(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d B", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1f KB", float64(n)/1000)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/1_000_000)
}

// libraryLines are the tab's lines: its sel line and each line's file.
func (pv *projectView) libraryLines(w int) ([]string, int, []int) {
	switch {
	case pv.libErr != nil:
		return []string{styleBad.Render("error: " + oneLine(pv.libErr.Error()))}, -1, nil
	case !pv.libOK:
		return []string{styleFaint.Render("loading…")}, -1, nil
	}
	files := pv.libFiles()
	if len(files) == 0 {
		return []string{styleFaint.Render("no files: a thread's report attaches them (tm report --attach)")}, -1, nil
	}
	var out []string
	var hits []int
	sel := -1
	group := ""
	for i, f := range files {
		if f.Thread != group {
			group = f.Thread
			if len(out) > 0 {
				out, hits = append(out, ""), append(hits, noHit)
			}
			out, hits = append(out, libGroup(f, w)), append(hits, noHit)
		}
		size := fmt.Sprintf("%9s", sizeWords(f.Size))
		l := "  " + fit(f.Name, max(w-len(size)-4, 4)) + "  " + size
		if i == pv.sel[tabLibrary] {
			sel = len(out)
			l = styleSel.Render(fit(l, w))
		} else {
			l = fit(l, w) + reset
		}
		out, hits = append(out, l), append(hits, i)
	}
	return out, sel, hits
}

// libraryKey: enter reads the selected file, d deletes it and D its
// thread's files, each after y.
func (pv *projectView) libraryKey(m *dash, k tea.KeyPressMsg) tea.Cmd {
	f, ok := pv.selLib()
	if !ok {
		return nil
	}
	switch k.String() {
	case "enter":
		return m.openLibFile(pv, f)
	case "d":
		m.confirm("Delete a library file", "Delete "+f.Name+" ("+sizeWords(f.Size)+") of thread "+f.Thread+"? It can't be brought back.", func() tea.Cmd {
			return m.libRemove(pv.slug, f.Thread, f.Name, "deleted "+f.Name)
		})
	case "D":
		n := 0
		for _, g := range pv.library {
			if g.Thread == f.Thread {
				n++
			}
		}
		m.confirm("Delete a thread's library", fmt.Sprintf("Delete all %d library files of thread %s? They can't be brought back.", n, f.Thread), func() tea.Cmd {
			return m.libRemove(pv.slug, f.Thread, "", fmt.Sprintf("deleted %d files of %s", n, f.Thread))
		})
	}
	return nil
}

func (m *dash) libRemove(slug, id, name, msg string) tea.Cmd {
	src := m.src
	// The action reloads the popup, library included.
	return m.act(func() actionMsg {
		if _, err := src.LibraryRemove(slug, id, name); err != nil {
			return actionMsg{err: err}
		}
		return actionMsg{msg: msg}
	})
}

// libMax is how much of a file the viewer reads.
const libMax = 1 << 20

// libView is one library file, read-only and scrollable over the popup.
type libView struct {
	pv     *projectView
	f      thread.LibFile
	body   string // text to show, "" for a file shown by name and size
	note   string
	scroll int
}

func (m *dash) openLibFile(pv *projectView, f thread.LibFile) tea.Cmd {
	lv := &libView{pv: pv, f: f}
	data, truncated, err := m.src.LibraryRead(pv.slug, f.Thread, f.Name, libMax)
	switch {
	case err != nil:
		lv.note = "can't read it: " + oneLine(err.Error())
	default:
		lv.body, lv.note = libText(f.Name, data, truncated)
	}
	m.push(lv)
	return nil
}

// libText is how a file shows: text as it is, JSON indented, anything
// else not at all (its note says so). Control characters never reach the
// terminal.
func libText(name string, data []byte, truncated bool) (string, string) {
	if bytes.IndexByte(data[:min(len(data), 8192)], 0) >= 0 || !utf8.Valid(trimPartial(data)) {
		return "", "not text: only its name and size show"
	}
	text := string(data)
	if strings.EqualFold(filepath.Ext(name), ".json") && !truncated {
		var buf bytes.Buffer
		if json.Indent(&buf, data, "", "  ") == nil {
			text = buf.String()
		}
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\t", "    ")
	note := ""
	if truncated {
		note = "shows the first " + sizeWords(libMax)
	}
	return strings.Map(func(r rune) rune {
		if r != '\n' && (r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, text), note
}

// trimPartial drops a multi-byte character a cut ended in the middle of.
func trimPartial(b []byte) []byte {
	for i := 1; i <= utf8.UTFMax && i <= len(b); i++ {
		if r, n := utf8.DecodeLastRune(b); r == utf8.RuneError && n <= 1 {
			b = b[:len(b)-1]
		} else {
			break
		}
	}
	return b
}

func (lv *libView) lines(w int) []string {
	var out []string
	if lv.body != "" {
		for _, l := range strings.Split(strings.TrimRight(lv.body, "\n"), "\n") {
			out = append(out, strings.Split(ansi.Hardwrap(l, max(w, 10), true), "\n")...)
		}
	}
	if lv.note != "" {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, styleFaint.Render(lv.note))
	}
	return out
}

func (lv *libView) key(m *dash, k tea.KeyPressMsg) tea.Cmd {
	if d, ok := scrollKeys[k.String()]; ok {
		lv.scroll = clampScroll(lv.scroll+d, len(lv.lines(m.inner(viewWidth))))
		return nil
	}
	if k.String() == "esc" {
		m.pop()
	}
	return nil
}

func (lv *libView) wheel(m *dash, d int) { lv.key(m, arrow(d)) }

func (lv *libView) render(m *dash) string {
	title := lv.f.Name + " · " + sizeWords(lv.f.Size)
	return m.popup(box{title: title, body: lv.lines(m.inner(viewWidth)), sel: -1, scroll: lv.scroll,
		keys: "↑ ↓ scroll · esc back"})
}
