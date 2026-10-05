package tui

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/theclifmeister/termilator/internal/emu"
	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/server"
	"github.com/theclifmeister/termilator/internal/view"
)

func TestParseChord(t *testing.T) {
	for in, want := range map[string]rune{`ctrl+\`: '\\', "Ctrl+]": ']', " ctrl+b ": 'b'} {
		c, err := parseChord(in)
		if err != nil || c.r != want {
			t.Errorf("parseChord(%q) = %q, %v; want %q", in, c.r, err, want)
		}
	}
	for _, in := range []string{"", "ctrl+", "alt+x", "ctrl+ab", "x", "ctrl+é"} {
		if _, err := parseChord(in); err == nil {
			t.Errorf("parseChord(%q) accepted", in)
		}
	}
	c, _ := parseChord(DefaultPrefixKey)
	if !c.match(uv.Key{Code: 'b', Mod: uv.ModCtrl}) {
		t.Error("ctrl+b does not match its key")
	}
	if c.match(uv.Key{Code: 'b', Mod: uv.ModCtrl | uv.ModShift}) || c.match(uv.Key{Code: 'b'}) {
		t.Error("prefix key matches other modifiers")
	}
	// The outer terminal sends Ctrl+B as 0x02, or as CSI 98;5u under kitty
	// "disambiguate": both are the prefix.
	for _, in := range []string{"\x02", "\x1b[98;5u"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		events := make(chan uv.Event, 4)
		go uv.NewTerminalReader(strings.NewReader(in), "xterm-256color").StreamEvents(ctx, events)
		select {
		case ev := <-events:
			if k, ok := ev.(uv.KeyPressEvent); !ok || !c.match(uv.Key(k)) {
				t.Errorf("%q decodes to %#v, not the prefix", in, ev)
			}
		case <-ctx.Done():
			t.Errorf("%q decodes to nothing", in)
		}
		cancel()
	}
}

func TestPrefixKeyFromConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERMILATOR_HOME", home)
	if c, err := prefixKey(); err != nil || c.r != 'b' {
		t.Fatalf("no config: %q %v", c.r, err)
	}
	path := filepath.Join(home, "config.toml")
	// detach is the older name of the same key.
	os.WriteFile(path, []byte("[keys]\ndetach = \"ctrl+]\"\n"), 0o600)
	if c, err := prefixKey(); err != nil || c.r != ']' {
		t.Fatalf("detach: %q %v", c.r, err)
	}
	os.WriteFile(path, []byte("[keys]\nprefix = \"ctrl+a\"\ndetach = \"ctrl+]\"\n"), 0o600)
	if c, err := prefixKey(); err != nil || c.r != 'a' || ConfigPrefix() != "ctrl+a" {
		t.Fatalf("prefix wins: %q %v", c.r, err)
	}
	os.WriteFile(path, []byte("[keys]\nprefix = \"F12\"\n"), 0o600)
	if c, err := prefixKey(); err == nil || c.r != 'b' {
		t.Fatalf("bad key: %q %v (want the default and an error)", c.r, err)
	}
}

func TestPrefixStep(t *testing.T) {
	p := chord{'\\'}
	pk := uv.Key{Code: '\\', Mod: uv.ModCtrl}
	key := func(s string) uv.Key { return uv.Key{Code: rune(s[0]), Text: s} }
	cases := []struct {
		name      string
		pending   bool
		k         uv.Key
		dashboard bool
		want      prefixDo
	}{
		{"a key goes to the program", false, key("x"), true, prefixDo{input: true}},
		{"the prefix arms", false, pk, true, prefixDo{arm: true}},
		{"prefix twice sends it", true, pk, true, prefixDo{input: true}},
		{"prefix d detaches", true, key("d"), false, prefixDo{detach: true}},
		{"prefix p detaches to the switcher", true, key("p"), true, prefixDo{detach: true, then: "p"}},
		{"prefix ] without a dashboard cancels", true, key("]"), false, prefixDo{}},
		{"prefix q cancels", true, key("q"), true, prefixDo{}},
		{"d alone is typed", false, key("d"), true, prefixDo{input: true}},
		{"prefix { narrows the sidebar", true, key("{"), false, prefixDo{pane: "{"}},
		{"prefix tab: the sidebar's keyboard", true, uv.Key{Code: uv.KeyTab}, false, prefixDo{pane: "tab"}},
		{"tab alone is typed", false, uv.Key{Code: uv.KeyTab}, false, prefixDo{input: true}},
		// Split panes are gone: their keys cancel.
		{"prefix % cancels", true, key("%"), false, prefixDo{}},
		{"prefix x cancels", true, key("x"), true, prefixDo{}},
		{"prefix z cancels", true, key("z"), true, prefixDo{}},
		{"prefix → cancels", true, uv.Key{Code: uv.KeyRight}, false, prefixDo{}},
		{"ctrl+→ is typed", false, uv.Key{Code: uv.KeyRight, Mod: uv.ModCtrl}, false, prefixDo{input: true}},
		{"prefix u takes over", true, key("u"), false, prefixDo{takeover: true}},
		{"prefix r toggles remote control", true, key("r"), false, prefixDo{remote: true}},
		{"u alone is typed", false, key("u"), true, prefixDo{input: true}},
	}
	for _, c := range cases {
		if got := prefixStep(p, c.pending, c.k, c.dashboard); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestToKey(t *testing.T) {
	cases := []struct {
		in   uv.Key
		want emu.Key
		ok   bool
	}{
		{uv.Key{Code: 'a', Text: "a"}, emu.Key{Rune: 'a', Text: "a"}, true},
		{uv.Key{Code: uv.KeyEnter, Mod: uv.ModShift}, emu.Key{Special: emu.KeyEnter, Mods: emu.ModShift}, true},
		{uv.Key{Code: 'x', Mod: uv.ModMeta}, emu.Key{Rune: 'x', Mods: emu.ModAlt}, true},
		{uv.Key{Code: uv.KeyF20}, emu.Key{}, false},
	}
	for _, c := range cases {
		got, ok := toKey(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("toKey(%v) = %+v %v; want %+v %v", c.in, got, ok, c.want, c.ok)
		}
	}
	m, ok := toMouse(uv.MouseWheelEvent{X: 3, Y: 4, Button: uv.MouseWheelDown})
	if !ok || m != (emu.Mouse{Action: emu.MousePress, Button: emu.MouseWheelDown, X: 3, Y: 4}) {
		t.Errorf("toMouse: %+v %v", m, ok)
	}
}

// TestWatchOnlyPane: a thread's pane takes no keys and says so; prefix u
// asks, and only y takes it over and tells the coordinator.
func TestWatchOnlyPane(t *testing.T) {
	c, err := newClient(server.Paths{}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer c.enc.Close()
	told := make(chan proto.SessionInfo, 1)
	c.prefix, c.statusBar = chord{'\\'}, true
	c.setWindow(160, 40)
	c.takeover = func(s proto.SessionInfo) error { told <- s; return nil }
	// No connection: a key sent to the program would panic.
	p := &pane{watch: true, info: proto.SessionInfo{ID: "s-2", Role: proto.RoleThread, Project: "demo", Thread: "t-0001"}}
	c.v = view.View{Mode: view.ModeLayout, Focus: "s-2"}
	c.panes["s-2"], c.focus = p, p
	c.status()
	if !strings.Contains(c.statusText, `watch-only, prefix+u takes over`) {
		t.Fatalf("status %q", c.statusText)
	}
	pk := uv.Key{Code: '\\', Mod: uv.ModCtrl}
	u := uv.Key{Code: 'u', Text: "u"}
	c.key(uv.Key{Code: 'x', Text: "x"})
	c.handle(uv.PasteEvent{Content: "hello"})

	// Anything but y keeps watching.
	c.key(pk)
	c.key(u)
	if c.confirm != p || !strings.Contains(c.statusText, "take over t-0001 and type into it?") {
		t.Fatalf("no question: %q", c.statusText)
	}
	c.key(uv.Key{Code: 'n', Text: "n"})
	if !p.watch || c.confirm != nil || !strings.Contains(c.statusText, "still watching") {
		t.Fatalf("n took over: %q", c.statusText)
	}

	c.key(pk)
	c.key(u)
	c.key(uv.Key{Code: 'y', Text: "y"})
	if p.watch || !strings.Contains(c.statusText, "taken over") {
		t.Fatalf("y: watch %v status %q", p.watch, c.statusText)
	}
	select {
	case s := <-told:
		if s.Thread != "t-0001" {
			t.Fatalf("told about %+v", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the coordinator wasn't told")
	}
}

// TestOuterModes: the outer terminal moves from one mouse tracking mode
// to the next without resetting one after setting another, which would
// stop all tracking; with a sidebar it always reports clicks and drags.
func TestOuterModes(t *testing.T) {
	mirror, err := emu.New(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer mirror.Close()
	c := &client{outer: map[int]bool{}, focus: &pane{mirror: mirror}}
	steps := []struct {
		program string // what the program writes
		side    bool
		want    string
	}{
		{"\x1b[?1003h\x1b[?1006h", false, "\x1b[?1003h\x1b[?1006h"},
		{"\x1b[?1003l\x1b[?1002h", false, "\x1b[?1003l\x1b[?1002h"},
		{"\x1b[?1002l", false, "\x1b[?1002l\x1b[?1006l"},
		{"", true, "\x1b[?1002h\x1b[?1006h"},
		{"\x1b[?1003h", true, "\x1b[?1002l\x1b[?1003h"},
		{"\x1b[?1003l", true, "\x1b[?1003l\x1b[?1002h"},
	}
	for i, s := range steps {
		mirror.Write([]byte(s.program))
		c.side = nil
		if s.side {
			c.side = &sidebar{}
		}
		if got := string(c.outerModes()); got != s.want {
			t.Errorf("step %d: %q, want %q", i, got, s.want)
		}
	}
}

// TestSidebarFocusNoLeak: prefix+tab gives the sidebar the keyboard;
// keys, paste and the prefix twice then never reach the pane (it has no
// connection: anything sent would panic), and esc or prefix+tab give
// the keyboard back.
func TestSidebarFocusNoLeak(t *testing.T) {
	c, err := newClient(server.Paths{}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer c.enc.Close()
	c.prefix, c.statusBar = chord{'\\'}, true
	c.setWindow(160, 40)
	p := &pane{info: proto.SessionInfo{ID: "s-1", Role: proto.RoleCoordinator, Project: "demo"}}
	c.v = view.View{Mode: view.ModeLayout, Focus: "s-1"}
	c.panes["s-1"], c.focus = p, p
	c.side, c.sideW = &sidebar{}, sideDefault
	pk := uv.Key{Code: '\\', Mod: uv.ModCtrl}
	tab := uv.Key{Code: uv.KeyTab}

	c.key(pk)
	c.key(tab)
	if !c.sideFocus || !strings.Contains(c.statusText, "sidebar: ↑ ↓ move") {
		t.Fatalf("prefix+tab: focus %v status %q", c.sideFocus, c.statusText)
	}
	for _, k := range []uv.Key{{Code: 'x', Text: "x"}, {Code: uv.KeyF5}, {Code: 'c', Mod: uv.ModCtrl}} {
		c.key(k)
	}
	c.handle(uv.PasteEvent{Content: "hello"})
	c.key(pk)
	c.key(pk) // the prefix twice: still not to the pane
	if !c.sideFocus {
		t.Fatal("lost the focus")
	}
	c.key(uv.Key{Code: uv.KeyEscape})
	if c.sideFocus || strings.Contains(c.statusText, "sidebar:") {
		t.Fatalf("esc: focus %v status %q", c.sideFocus, c.statusText)
	}
	c.key(pk)
	c.key(tab)
	c.key(pk)
	c.key(tab)
	if c.sideFocus {
		t.Fatal("prefix+tab didn't give the keyboard back")
	}
	// No sidebar: prefix+tab says so.
	c.side, c.sideW = nil, 0
	c.key(pk)
	c.key(tab)
	if c.sideFocus || !strings.Contains(c.statusText, "no sidebar") {
		t.Fatalf("no sidebar: focus %v status %q", c.sideFocus, c.statusText)
	}
}
