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

	"github.com/theclifmeister/termalator/internal/emu"
	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/server"
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
	t.Setenv("TERMALATOR_HOME", home)
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
	ctrlRight := uv.Key{Code: uv.KeyRight, Mod: uv.ModCtrl}
	cases := []struct {
		name            string
		pending, repeat bool
		k               uv.Key
		dashboard       bool
		want            prefixDo
	}{
		{"a key goes to the program", false, false, key("x"), true, prefixDo{input: true}},
		{"the prefix arms", false, false, pk, true, prefixDo{arm: true}},
		{"prefix twice sends it", true, false, pk, true, prefixDo{input: true}},
		{"prefix d detaches", true, false, key("d"), false, prefixDo{detach: true}},
		{"prefix p detaches to the switcher", true, false, key("p"), true, prefixDo{detach: true, then: "p"}},
		{"prefix ] without a dashboard cancels", true, false, key("]"), false, prefixDo{}},
		{"prefix x closes the pane", true, false, key("x"), true, prefixDo{pane: "x"}},
		{"prefix q cancels", true, false, key("q"), true, prefixDo{}},
		{"d alone is typed", false, false, key("d"), true, prefixDo{input: true}},
		{"prefix % splits", true, false, key("%"), false, prefixDo{pane: "%"}},
		{`prefix " splits`, true, false, key(`"`), false, prefixDo{pane: `"`}},
		{"prefix space switches layout", true, false, uv.Key{Code: uv.KeySpace, Text: " "}, false, prefixDo{pane: "space"}},
		{"prefix → moves the focus", true, false, uv.Key{Code: uv.KeyRight}, false, prefixDo{pane: "right"}},
		{"prefix ctrl+→ resizes", true, false, ctrlRight, false, prefixDo{pane: "ctrl+right"}},
		{"ctrl+→ repeats without the prefix", false, true, ctrlRight, false, prefixDo{pane: "ctrl+right"}},
		{"ctrl+→ without the repeat is typed", false, false, ctrlRight, false, prefixDo{input: true}},
		{"→ never repeats", false, true, uv.Key{Code: uv.KeyRight}, false, prefixDo{input: true}},
		{"prefix u takes over", true, false, key("u"), false, prefixDo{takeover: true}},
		{"prefix r toggles remote control", true, false, key("r"), false, prefixDo{remote: true}},
		{"u alone is typed", false, false, key("u"), true, prefixDo{input: true}},
	}
	for _, c := range cases {
		if got := prefixStep(p, c.pending, c.repeat, c.k, c.dashboard); got != c.want {
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
	c.root, c.focus = &node{leaf: p}, p
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
