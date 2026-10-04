package tui

import (
	"os"
	"path/filepath"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/theclifmeister/termalator/internal/emu"
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
	c, _ := parseChord(DefaultDetachKey)
	if !c.match(uv.Key{Code: '\\', Mod: uv.ModCtrl}) {
		t.Error("ctrl+\\ does not match its key")
	}
	if c.match(uv.Key{Code: '\\', Mod: uv.ModCtrl | uv.ModShift}) || c.match(uv.Key{Code: '\\'}) {
		t.Error("detach key matches other modifiers")
	}
}

func TestDetachKeyFromConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERMALATOR_HOME", home)
	if c, err := detachKey(); err != nil || c.r != '\\' {
		t.Fatalf("no config: %q %v", c.r, err)
	}
	path := filepath.Join(home, "config.toml")
	os.WriteFile(path, []byte("[keys]\ndetach = \"ctrl+]\"\n"), 0o600)
	if c, err := detachKey(); err != nil || c.r != ']' {
		t.Fatalf("configured: %q %v", c.r, err)
	}
	os.WriteFile(path, []byte("[keys]\ndetach = \"F12\"\n"), 0o600)
	if c, err := detachKey(); err == nil || c.r != '\\' {
		t.Fatalf("bad key: %q %v (want the default and an error)", c.r, err)
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
