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
	c, _ := parseChord(DefaultPrefixKey)
	if !c.match(uv.Key{Code: '\\', Mod: uv.ModCtrl}) {
		t.Error("ctrl+\\ does not match its key")
	}
	if c.match(uv.Key{Code: '\\', Mod: uv.ModCtrl | uv.ModShift}) || c.match(uv.Key{Code: '\\'}) {
		t.Error("prefix key matches other modifiers")
	}
}

func TestPrefixKeyFromConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TERMALATOR_HOME", home)
	if c, err := prefixKey(); err != nil || c.r != '\\' {
		t.Fatalf("no config: %q %v", c.r, err)
	}
	path := filepath.Join(home, "config.toml")
	// detach is the older name of the same key.
	os.WriteFile(path, []byte("[keys]\ndetach = \"ctrl+]\"\n"), 0o600)
	if c, err := prefixKey(); err != nil || c.r != ']' {
		t.Fatalf("detach: %q %v", c.r, err)
	}
	os.WriteFile(path, []byte("[keys]\nprefix = \"ctrl+b\"\ndetach = \"ctrl+]\"\n"), 0o600)
	if c, err := prefixKey(); err != nil || c.r != 'b' || ConfigPrefix() != "ctrl+b" {
		t.Fatalf("prefix wins: %q %v", c.r, err)
	}
	os.WriteFile(path, []byte("[keys]\nprefix = \"F12\"\n"), 0o600)
	if c, err := prefixKey(); err == nil || c.r != '\\' {
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
		{"prefix x cancels", true, key("x"), true, prefixDo{}},
		{"d alone is typed", false, key("d"), true, prefixDo{input: true}},
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
