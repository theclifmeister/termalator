package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The key table (docs/SPEC.md §4): prefix then a key means the same
// everywhere, and no plain key on any screen means something else than
// the prefix command of the same key.

// isPrefixCommand says whether key is a command after the prefix.
func isPrefixCommand(key string) bool {
	return key == "d" || key == "q" || key == "r" || prefixCommands[key] || paneCommands[key]
}

// prefixKeysAll are every prefix command's key but the prefix itself.
func prefixKeysAll() []string {
	keys := []string{"d", "q", "r"}
	for k := range prefixCommands {
		keys = append(keys, k)
	}
	for k := range paneCommands {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// popupStates are the screens a plain key can reach besides the list,
// each opened on a fresh dashboard: every popup (the project popup on
// each tab, the task list and a shown task in each state that has keys),
// the menu, and the sidebar and the details panel holding the keyboard.
// Questions and prompts take any key (y or no, typed text) and are left
// out; the prefix answers them no.
var popupStates = map[string]func(m *dash){
	"the help":           func(m *dash) { keyPress(m, "?") },
	"the inbox":          func(m *dash) { keyPress(m, "i") },
	"the settings":       func(m *dash) { keyPress(m, ",") },
	"the menu":           func(m *dash) { m.openMenu("", m.dashItems(), 0, m.bodyRows()) },
	"overview tab":       func(m *dash) { m.Update(keyPress(m, "a")()); keyPress(m, "1") },
	"inbox tab":          func(m *dash) { m.Update(keyPress(m, "a")()); keyPress(m, "2") },
	"tasks tab, review":  func(m *dash) { m.Update(keyPress(m, "a")()); keyPress(m, "3") },
	"tasks tab, blocked": func(m *dash) { m.Update(keyPress(m, "a")()); keyPress(m, "3"); keyPress(m, "down") },
	"tasks tab, open":    func(m *dash) { m.Update(keyPress(m, "a")()); keyPress(m, "3"); keyPress(m, "up"); keyPress(m, "up") },
	"settings tab":       func(m *dash) { m.Update(keyPress(m, "a")()); keyPress(m, "4") },
	"keys tab":           func(m *dash) { m.Update(keyPress(m, "a")()); keyPress(m, "5") },
	"memory tab":         func(m *dash) { m.Update(keyPress(m, "a")()); keyPress(m, "6") },
	"t list, review":     func(m *dash) { m.Update(keyPress(m, "t")()) },
	"t list, blocked":    func(m *dash) { m.Update(keyPress(m, "t")()); keyPress(m, "down") },
	"review task, shown": func(m *dash) { m.Update(keyPress(m, "t")()); keyPress(m, "enter") },
	"blocked task, shown": func(m *dash) {
		m.Update(keyPress(m, "t")())
		keyPress(m, "down")
		keyPress(m, "enter")
	},
}

// TestNoPlainKeyIsAPrefixCommand: a plain key that is also a prefix
// command means the same as it, on every screen. On the list it is the
// same dashboard key (prefix then a dashboard key runs it); the sidebar's
// own keys are none of them; and in a popup or the menu such a key does
// nothing at all: popups close with esc only, and their own keys (the
// tasks' D A x c) are not prefix commands.
func TestNoPlainKeyIsAPrefixCommand(t *testing.T) {
	// The list: a plain key that is a prefix command is the dashboard
	// key prefix then that key runs.
	for _, a := range actions {
		for _, k := range a.keys {
			if isPrefixCommand(k) && !prefixCommands[k] && !paneCommands[k] {
				t.Errorf("dashboard key %q is prefix+%s, which means something else", k, k)
			}
		}
	}
	for _, a := range sideActions {
		for _, k := range a.keys {
			if isPrefixCommand(k) {
				t.Errorf("sidebar key %q is a prefix command", k)
			}
		}
	}
	for _, k := range prefixKeysAll() {
		_, plain := needsYouData(t, 86+sideDefault)
		_, prefixed := needsYouData(t, 86+sideDefault)
		keyPress(plain, k)
		prefixed.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
		keyPress(prefixed, k)
		if k == "q" || k == "d" || k == "r" {
			// Not dashboard keys: plain they do nothing.
			if plain.quitting || plain.top() != nil || whole(plain) != whole(needsYou(t)) {
				t.Errorf("plain %s on the list does something", k)
			}
			continue
		}
		if a, b := whole(plain), whole(prefixed); a != b || plain.focus != prefixed.focus {
			t.Errorf("on the list, %s and prefix+%s differ:\n%s\n---\n%s", k, k, a, b)
		}
	}

	// Every other screen: a plain prefix-command key does nothing.
	for name, open := range popupStates {
		for _, k := range prefixKeysAll() {
			src, m := needsYouData(t, 86+sideDefault)
			open(m)
			before, stack, msg := whole(m), slices.Clone(m.stack), m.msg
			cmd := keyPress(m, k)
			if cmd != nil {
				m.Update(cmd())
			}
			if whole(m) != before || !slices.Equal(m.stack, stack) || m.msg != msg || m.quitting ||
				len(src.asked)+len(src.delegated)+len(src.opened)+len(src.remote) > 0 {
				t.Errorf("%s: plain %s did something (it is prefix+%s):\n%s", name, k, k, whole(m))
			}
		}
	}
}

// needsYou is needsYouData's dashboard, untouched.
func needsYou(t *testing.T) *dash {
	_, m := needsYouData(t, 86+sideDefault)
	return m
}

// TestPrefixNeverReachesPopup: prefix then a key over any popup is that
// prefix command, as from the list: the popup closes first and never
// gets the key (prefix+a on the Tasks tab opens the project popup anew
// rather than accepting a task).
func TestPrefixNeverReachesPopup(t *testing.T) {
	want := map[string]string{"a": "*tui.projectView", "i": "*tui.projectView", "t": "*tui.projectView",
		",": "*tui.settingsView", "?": "*tui.helpView", "d": "<nil>", "tab": "<nil>",
		"r": "*tui.confirmView"}
	states := map[string]func(m *dash){"the list": func(*dash) {}}
	for name, open := range popupStates {
		states[name] = open
	}
	for name, open := range states {
		for k, top := range want {
			src, m := needsYouData(t, 86+sideDefault)
			open(m)
			m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
			keyPress(m, k)
			if got := fmt.Sprintf("%T", m.top()); got != top || len(m.stack) > 1 {
				t.Errorf("%s: prefix+%s left %s (%d open), want %s alone", name, k, got, len(m.stack), top)
			}
			if len(src.asked)+len(src.delegated) > 0 {
				t.Errorf("%s: prefix+%s reached the popup: asked %v %v", name, k, src.asked, src.delegated)
			}
			if k == "tab" && m.focus == areaMain {
				t.Errorf("%s: prefix+tab kept the keyboard on the list", name)
			}
		}
	}
}

// TestPrefixRemote: prefix+r on the dashboard asks, then turns remote
// control of the selected project's coordinator on; a project with no
// coordinator running says so.
func TestPrefixRemote(t *testing.T) {
	src, m := needsYouData(t, 86+sideDefault)
	m.sel = "p:alpha"
	m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	keyPress(m, "r")
	cv, ok := m.top().(*confirmView)
	if !ok || !strings.HasPrefix(cv.question, "Turn remote control on for alpha") {
		t.Fatalf("prefix+r: %T", m.top())
	}
	act(m, src, "y")
	if !slices.Equal(src.remote, []string{"alpha on"}) || m.msg != "remote control on for alpha" {
		t.Fatalf("remote %v, %q", src.remote, m.msg)
	}
	src.data.Sessions = src.data.Sessions[1:] // alpha's coordinator stops
	m.setData(src.data)
	m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	keyPress(m, "r")
	if m.top() != nil || !strings.Contains(m.msg, "no coordinator runs for alpha") {
		t.Fatalf("prefix+r without a coordinator: %T, %q", m.top(), m.msg)
	}
}

// TestPrefixOverSession: over a session, prefix+r and prefix+tab close
// the popup and go back to the session, which runs them; prefix+a there
// opens the project popup anew; an unknown key says so.
func TestPrefixOverSession(t *testing.T) {
	src := &fakeSource{data: testData()}
	for _, k := range []string{"r", "tab"} {
		m := newDash(DashOptions{Source: src, Width: 100, Height: 30,
			Over: &Over{Key: "a", Project: "alpha", Session: "s-1", Screen: []string{"", "row"}}})
		m.setData(src.data)
		if _, ok := m.top().(*projectView); !ok {
			t.Fatalf("over: %T", m.top())
		}
		m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
		if !quits(keyPress(m, k)) || m.result.Command != k || m.result.Attach != "s-1" {
			t.Errorf("prefix+%s over a session: %+v", k, m.result)
		}
	}
	m := newDash(DashOptions{Source: src, Width: 100, Height: 30,
		Over: &Over{Key: "t", Project: "alpha", Session: "s-1", Screen: []string{"", "row"}}})
	m.setData(src.data)
	m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	keyPress(m, "a")
	if _, ok := m.top().(*projectView); !ok || len(m.stack) != 1 || m.result.Attach != "" {
		t.Fatalf("prefix+a over the tasks: %T (%d open), %+v", m.top(), len(m.stack), m.result)
	}
	m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	keyPress(m, "z")
	if m.msg != "prefix+z isn't a command; ? lists them" {
		t.Fatalf("prefix+z: %q", m.msg)
	}
}
