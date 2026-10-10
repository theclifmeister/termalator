package tui

import (
	"io"
	"log"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
	"github.com/theclifmeister/terminatr/internal/view"
)

// The dialog anatomy (docs/STYLE.md): every popup carries its title in
// its top border and its own keys in an action row inside its frame;
// the footer under it shows none of them; the scene under it, the
// sidebar too, dims.

// boxText is the open popup's lines, as text, from its top border to its
// bottom one.
func boxText(t *testing.T, m *dash) []string {
	t.Helper()
	m.render()
	g := m.geo
	if g == nil {
		t.Fatal("no popup drawn")
	}
	lines := strings.Split(whole(m), "\n")[g.y : g.y+g.h]
	for i, l := range lines {
		r := []rune(l)
		lines[i] = string(r[g.x : g.x+g.w])
	}
	return lines
}

// TestDialogAnatomy: each kind of popup has its title in its border and
// its keys on its last row inside the frame, and the footer under it
// lists none of them.
func TestDialogAnatomy(t *testing.T) {
	for _, tc := range []struct {
		name, title, keys string
		open              func(m *dash)
	}{
		{"help", "Help", "↑ ↓ scroll · esc close", func(m *dash) { keyPress(m, "?") }},
		{"menu", "Menu", "enter pick · esc close", func(m *dash) { m.openMenu("", m.dashItems(), 2, 2) }},
		{"settings", "Settings", "esc close", func(m *dash) { keyPress(m, ",") }},
		{"new project", "New project", "enter ok · ctrl+u clear · esc cancel", func(m *dash) { keyPress(m, "n") }},
		{"inbox", "alpha", "esc close", func(m *dash) { keyPress(m, "i") }},
		{"confirm", "Sure", "y yes · n no · esc cancel", func(m *dash) { m.confirm("Sure", "Really?", func() tea.Cmd { return nil }) }},
	} {
		_, m := popupData(t)
		tc.open(m)
		box := boxText(t, m)
		if !strings.HasPrefix(box[0], "╭─ "+tc.title+" ─") {
			t.Errorf("%s: top border %q", tc.name, box[0])
		}
		last := box[len(box)-2] // the row above the bottom border
		if !strings.HasPrefix(last, "│ ") || !strings.Contains(last, tc.keys) {
			t.Errorf("%s: last row %q lacks %q", tc.name, last, tc.keys)
		}
		if blank := box[len(box)-3]; strings.Trim(blank, "│ ") != "" {
			t.Errorf("%s: no blank row above the action row: %q", tc.name, blank)
		}
		lines := strings.Split(screen(m), "\n")
		if foot := lines[len(lines)-2]; strings.TrimSpace(string([]rune(foot)[m.sideW():])) != "" {
			t.Errorf("%s: the footer lists keys under the popup: %q", tc.name, foot)
		}
		for i, l := range strings.Split(m.render(), "\n") {
			if side := ansi.Cut(l, 0, m.sideW()); strings.TrimSpace(ansi.Strip(side)) != "" && !strings.HasPrefix(side, "\x1b[2m") {
				t.Errorf("%s: sidebar row %d isn't dimmed: %q", tc.name, i, side)
				break
			}
		}
	}
}

// TestConfirmKeys (K1): a yes/no question takes y, n and esc; enter and
// any other key do nothing.
func TestConfirmKeys(t *testing.T) {
	_, m := popupData(t)
	yes := 0
	m.confirmNo("Do it", "Do it?", "not done", func() tea.Cmd { yes++; return nil })
	cv := m.top()
	for _, k := range []string{"enter", "space", "q", "x", "Y"} {
		keyPress(m, k)
		if m.top() != cv || yes != 0 {
			t.Fatalf("%s answered the question (yes %d, top %T)", k, yes, m.top())
		}
	}
	keyPress(m, "n")
	if m.top() == cv || m.msg != "not done" || yes != 0 {
		t.Fatalf("n: top %T, msg %q, yes %d", m.top(), m.msg, yes)
	}
	m.confirmNo("Do it", "Do it?", "not done", func() tea.Cmd { yes++; return nil })
	keyPress(m, "esc")
	if m.top() != nil && m.top() == cv || m.msg != "not done" || yes != 0 {
		t.Fatalf("esc: msg %q, yes %d", m.msg, yes)
	}
	m.confirmNo("Do it", "Do it?", "not done", func() tea.Cmd { yes++; return nil })
	keyPress(m, "y")
	if yes != 1 {
		t.Fatalf("y: yes %d", yes)
	}
}

// TestActionRowClicks: the action row's hints are buttons: a click on
// "n no" answers no, on "y yes" yes.
func TestActionRowClicks(t *testing.T) {
	_, m := popupData(t)
	yes := 0
	for _, tc := range []struct {
		hint string
		want int
	}{{"n no", 0}, {"y yes", 1}} {
		m.confirmNo("Do it", "Do it?", "not done", func() tea.Cmd { yes++; return nil })
		clickOn(t, m, tc.hint)
		if m.top() != nil || yes != tc.want {
			t.Fatalf("click on %s: top %T, yes %d", tc.hint, m.top(), yes)
		}
	}
}

// TestPopupStacks: a popup opened from another draws over it, the one
// under it still in view (dimmed), and esc goes back to it.
func TestPopupStacks(t *testing.T) {
	_, m := popupData(t)
	m.Update(keyPress(m, "a")())
	keyPress(m, "3")
	keyPress(m, "enter") // the task, over the Tasks tab
	out := screen(m)
	if !strings.Contains(out, "─ Task · alpha ─") {
		t.Fatalf("the task view doesn't draw over the project popup:\n%s", out)
	}
	keyPress(m, "esc")
	if _, ok := m.top().(*projectView); !ok {
		t.Fatalf("esc: %T", m.top())
	}
}

// TestMessageClears: a footer message is about the last key: the next
// one clears it.
func TestMessageClears(t *testing.T) {
	_, m := popupData(t)
	m.confirmNo("Do it", "Do it?", "not done", func() tea.Cmd { return nil })
	keyPress(m, "n")
	if m.msg != "not done" {
		t.Fatalf("msg %q", m.msg)
	}
	keyPress(m, "down")
	if m.msg != "" {
		t.Fatalf("msg after the next key: %q", m.msg)
	}
}

// TestSessionDialog: in a session prefix+r asks in a dialog over the
// panes, with its keys inside it, never in the status bar; n says no,
// enter does nothing, and a click on its y yes says yes.
func TestSessionDialog(t *testing.T) {
	pk := uv.Key{Code: '\\', Mod: uv.ModCtrl}
	c, err := newClient(server.Paths{}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer c.enc.Close()
	c.prefix, c.statusBar, c.dashboard = chord{'\\'}, true, true
	c.side, c.sideW = &sidebar{}, sideDefault
	c.setWindow(160, 40)
	c.focus = &pane{info: proto.SessionInfo{ID: "s-1", Role: proto.RoleCoordinator, Project: "demo"}}
	c.v = view.View{Mode: view.ModeLayout, Focus: "s-1"}
	c.key(pk)
	c.key(uv.Key{Code: 'r', Text: "r"})
	if c.confirm == nil || c.dialog == nil {
		t.Fatal("prefix+r opened no dialog")
	}
	text := ansi.Strip(strings.Join(c.dialog.lines(), "\n"))
	for _, want := range []string{"╭─ Remote control ─", "Turn remote control on for demo", "y yes · n no · esc cancel"} {
		if !strings.Contains(text, want) {
			t.Errorf("dialog lacks %q:\n%s", want, text)
		}
	}
	c.mu.Lock()
	c.status()
	bar := ansi.Strip(c.statusText)
	c.mu.Unlock()
	if strings.Contains(bar, "y yes") || strings.Contains(bar, "remote control on for") {
		t.Errorf("the status bar asks: %q", bar)
	}
	c.key(uv.Key{Code: uv.KeyEnter})
	if c.confirm == nil {
		t.Fatal("enter answered the question")
	}
	c.key(uv.Key{Code: 'n', Text: "n"})
	if c.confirm != nil || c.dialog != nil || c.flash != "remote control unchanged" {
		t.Fatalf("n: confirm %v, dialog %v, flash %q", c.confirm != nil, c.dialog, c.flash)
	}
	// A click on its n no answers no too.
	c.key(pk)
	c.key(uv.Key{Code: 'r', Text: "r"})
	d := c.dialog
	// A click inside it, off its buttons, leaves it open.
	c.mouse(uv.MouseClickEvent{X: d.x + 3, Y: d.y + 1, Button: uv.MouseLeft})
	if c.confirm == nil {
		t.Fatal("a click on the question's text closed it")
	}
	lines := d.lines()
	row := len(lines) - 2 // the action row
	col := strings.Index(ansi.Strip(lines[row]), "n no")
	c.mouse(uv.MouseClickEvent{X: d.x + len([]rune(ansi.Strip(lines[row])[:col])), Y: d.y + row, Button: uv.MouseLeft})
	if c.confirm != nil {
		t.Fatal("a click on n no left the question open")
	}
}

// TestPopupSizes: a view is four fifths of the window's width and
// height, uncapped; a dialog
// is 64×12; both are centred on the whole window, and shrink to fit with
// a cell of margin.
func TestPopupSizes(t *testing.T) {
	for _, tc := range []struct {
		w, h       int
		view, conf [2]int
	}{
		{200, 60, [2]int{160, 48}, [2]int{64, 12}},
		{120, 40, [2]int{96, 32}, [2]int{64, 12}},
		{100, 30, [2]int{80, 24}, [2]int{64, 12}},
		{40, 14, [2]int{32, 11}, [2]int{38, 12}},
	} {
		for _, open := range []struct {
			name string
			want [2]int
			key  string
		}{{"view", tc.view, "?"}, {"dialog", tc.conf, ""}} {
			_, m := popupData(t)
			m.Update(tea.WindowSizeMsg{Width: tc.w, Height: tc.h})
			if open.key != "" {
				keyPress(m, open.key)
			} else {
				m.stack = append(m.stack, &confirmView{title: "Sure?", question: "Really?"})
			}
			m.render()
			g := m.geo
			if g == nil || g.w != open.want[0] || g.h != open.want[1] {
				t.Fatalf("%dx%d %s: box %+v, want %v", tc.w, tc.h, open.name, g, open.want)
			}
			if g.x != (tc.w-g.w)/2 || g.y != (tc.h-g.h)/2 {
				t.Errorf("%dx%d %s: at %d,%d, not centred on the window", tc.w, tc.h, open.name, g.x, g.y)
			}
		}
	}
}
