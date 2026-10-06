package tui

import (
	"io"
	"log"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/theclifmeister/terminatr/internal/proto"
	"github.com/theclifmeister/terminatr/internal/server"
	"github.com/theclifmeister/terminatr/internal/view"
)

// quits says whether cmd ends the program.
func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.QuitMsg:
		return true
	case tea.BatchMsg:
		return slices.ContainsFunc(msg, quits)
	}
	return false
}

// TestDashboardPrefixQuits: prefix+q quits the console from everywhere on
// the dashboard (the list, every popup, a prompt, a question, the ≡ menu,
// the sidebar and the details panel holding the keyboard, a popup over a
// session); plain q quits nowhere.
func TestDashboardPrefixQuits(t *testing.T) {
	src := &fakeSource{data: testData()}
	prefix := tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl}
	q := tea.KeyPressMsg{Code: 'q', Text: "q"}
	places := map[string]func(m *dash){
		"the list":          func(*dash) {},
		"the project popup": func(m *dash) { press(m, "a") },
		"the inbox":         func(m *dash) { press(m, "i") },
		"the tasks":         func(m *dash) { press(m, "t") },
		"the settings":      func(m *dash) { press(m, ",") },
		"the help":          func(m *dash) { press(m, "?") },
		"the switcher":      func(m *dash) { press(m, "p") },
		"the keys tab":      func(m *dash) { press(m, "a", "5") },
		"a prompt":          func(m *dash) { press(m, "n") },
		"a question": func(m *dash) {
			m.confirm("Sure", "sure?", func() tea.Cmd { return nil })
		},
		"the menu":    func(m *dash) { m.openMenu("", m.dashItems(), 0, m.bodyRows()) },
		"the sidebar": func(m *dash) { m.focus = areaSide },
		"the details": func(m *dash) { m.focus = areaDetails },
	}
	for name, open := range places {
		m := newDash(DashOptions{Source: src, Width: 160, Height: 40, State: DashState{Current: "alpha"}})
		m.setData(src.data)
		m.sel = "p:alpha"
		open(m)
		top := m.top()
		if _, cmd := m.Update(q); quits(cmd) || m.quitting {
			t.Errorf("%s: plain q quit", name)
		}
		// Plain q may close a popup, as esc does: open it again.
		if m.top() != top {
			m = newDash(DashOptions{Source: src, Width: 160, Height: 40, State: DashState{Current: "alpha"}})
			m.setData(src.data)
			m.sel = "p:alpha"
			open(m)
		}
		m.Update(prefix)
		if _, cmd := m.Update(q); !quits(cmd) || m.result.Attach != "" {
			t.Errorf("%s: prefix q didn't quit (result %+v)", name, m.result)
		}
	}

	// Over a session: the console quits instead of attaching again.
	m := newDash(DashOptions{Source: src, Width: 100, Height: 30,
		Over: &Over{Key: "i", Project: "alpha", Session: "s-5", Screen: []string{"", "row"}}})
	m.setData(src.data)
	m.Update(prefix)
	if _, cmd := m.Update(q); !quits(cmd) || m.result.Attach != "" {
		t.Fatalf("over a session: prefix q result %+v", m.result)
	}

	// The mouse: the footer's prefix+q button and the menu's quit.
	m = newDash(DashOptions{Source: src, Width: 160, Height: 40})
	m.setData(src.data)
	foot := m.footKeys()
	if !slices.ContainsFunc(hints(foot, 0), func(h hint) bool { return h.key == "prefix+q" }) {
		t.Fatalf("footer %q has no prefix+q button", foot)
	}
	if !quits(m.pressKey("prefix+q")) {
		t.Fatal("the footer's prefix+q didn't quit")
	}
	m = newDash(DashOptions{Source: src, Width: 160, Height: 40})
	m.setData(src.data)
	for _, it := range m.dashItems() {
		if it.label == "quit" {
			if it.key != "prefix+q" || !quits(it.run(m)) {
				t.Fatalf("menu quit: key %q", it.key)
			}
			return
		}
	}
	t.Fatal("the menu has no quit")
}

// TestAttachPrefixQuits: in a session prefix+q ends this console (Quit,
// the session still running) from the pane, the sidebar holding the
// keyboard, the ≡ menu and a question, in a full view and a bare one.
func TestAttachPrefixQuits(t *testing.T) {
	pk := uv.Key{Code: '\\', Mod: uv.ModCtrl}
	q := uv.Key{Code: 'q', Text: "q"}
	for _, bare := range []bool{false, true} {
		for name, open := range map[string]func(c *client){
			"the pane":    func(*client) {},
			"the sidebar": func(c *client) { c.key(pk); c.key(uv.Key{Code: uv.KeyTab}) },
			"the menu": func(c *client) {
				c.mu.Lock()
				c.openMenu("", c.sessionItems(), c.sideW, c.rows-1, true)
				c.mu.Unlock()
			},
			"a question": func(c *client) { c.key(pk); c.key(uv.Key{Code: 'r', Text: "r"}) },
		} {
			c, err := newClient(server.Paths{}, log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatal(err)
			}
			c.prefix, c.statusBar, c.bare, c.dashboard = chord{'\\'}, true, bare, !bare
			c.side, c.sideW = &sidebar{}, sideDefault
			c.setWindow(160, 40)
			// The focused pane isn't among c.panes, so the quit has no
			// stream to write to.
			c.focus = &pane{info: proto.SessionInfo{ID: "s-1", Role: proto.RoleCoordinator, Project: "demo"}}
			c.v = view.View{Mode: view.ModeLayout, Focus: "s-1"}
			open(c)
			if name == "the sidebar" && c.kb != areaSide || name == "the menu" && c.menu == nil || name == "a question" && c.confirmRemote == nil {
				t.Fatalf("bare %v, %s: didn't open", bare, name)
			}
			c.key(pk)
			c.key(q)
			select {
			case <-c.end:
			default:
				t.Fatalf("bare %v, %s: prefix q didn't end the attach", bare, name)
			}
			if r := c.result; !r.Quit || !r.Detached || r.Then != "" || r.Over != nil || r.GoTo != nil {
				t.Fatalf("bare %v, %s: result %+v", bare, name, r)
			}
			c.enc.Close()
		}
	}
}

// TestEveryPrefixCommandListed: every key that does something after the
// prefix in a session is in the help, the ≡ menu, the status bar's list
// after the prefix and the dashboard's footer after the prefix, so none
// is hidden.
func TestEveryPrefixCommandListed(t *testing.T) {
	var cmds []string
	for r := rune(0x21); r < 0x7f; r++ {
		if do := prefixStep(chord{'\\'}, true, uv.Key{Code: r, Text: string(r)}, true); do != (prefixDo{}) && !do.input {
			cmds = append(cmds, string(r))
		}
	}
	cmds = append(cmds, "tab")
	if !slices.Contains(cmds, "q") || !slices.Contains(cmds, "d") {
		t.Fatalf("prefix commands %q", cmds)
	}
	var help []string
	for _, k := range sessionKeys {
		f := strings.Fields(k.keys)
		f[0] = strings.TrimPrefix(f[0], "prefix+")
		help = append(help, f...)
	}
	var menu []string
	for _, e := range sessionMenu {
		menu = append(menu, e.key)
	}
	_, hs := statusBar(proto.SessionInfo{ID: "s-1"}, nil, true, 400, "")
	var bar []string
	for _, h := range hs {
		bar = append(bar, h.key)
	}
	hint := strings.Fields(prefixHint)
	for _, k := range prefixKeysAll() {
		if !slices.Contains(cmds, k) {
			t.Errorf("prefix+%s works on the dashboard but not in a session", k)
		}
	}
	for _, k := range cmds {
		if !slices.Contains(hint, k) {
			t.Errorf("prefix+%s isn't in the dashboard's footer after the prefix (prefixHint)", k)
		}
		if !slices.Contains(help, k) {
			t.Errorf("prefix+%s isn't in the help (sessionKeys)", k)
		}
		if !slices.Contains(menu, k) {
			t.Errorf("prefix+%s isn't in the ≡ menu (sessionMenu)", k)
		}
		if !slices.Contains(bar, k) {
			t.Errorf("prefix+%s isn't in the status bar after the prefix", k)
		}
	}
	// No plain key quits the dashboard: quitting is prefix+q's.
	for _, a := range actions {
		if a.menu != nil && a.menu[0] == "quit" && !slices.Equal(a.keys, []string{"prefix+q"}) {
			t.Errorf("quit's keys %q, want prefix+q only", a.keys)
		}
	}
}
