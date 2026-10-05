package tui

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/theclifmeister/termilator/internal/config"
	"github.com/theclifmeister/termilator/internal/emu"
)

// DefaultPrefixKey is Ctrl+B, as in tmux. Outer terminals send it as
// 0x02, or as CSI 98;5u once the client has pushed kitty "disambiguate";
// ultraviolet decodes both to the same key. Inside tmux, which takes
// Ctrl+B itself, users set another one in the settings popup (,), which
// writes [keys] prefix in config.toml.
//
// The prefix starts a key command, as in tmux (docs/SPEC.md §4): in a
// session, prefix then d returns to the dashboard, prefix then a, i, t,
// , or ? opens that popup over the session, prefix then p, ] or [
// returns and runs it there, and prefix twice sends the prefix itself to
// the program.
const DefaultPrefixKey = "ctrl+b"

// chord is a Ctrl+<character> key combination.
type chord struct{ r rune }

func (c chord) match(k uv.Key) bool { return k.Mod == uv.ModCtrl && k.Code == c.r }

func (c chord) String() string { return "ctrl+" + string(c.r) }

// parseChord reads "ctrl+<character>", the form config.toml uses.
func parseChord(s string) (chord, error) {
	rest, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(s)), "ctrl+")
	r, n := utf8.DecodeRuneInString(rest)
	if !ok || n == 0 || n != len(rest) || r < 0x20 || r >= 0x7f {
		return chord{}, fmt.Errorf("prefix key %q: want ctrl+<character>, e.g. %q", s, DefaultPrefixKey)
	}
	return chord{r}, nil
}

// prefixKey reads [keys] prefix from config.toml, or the older [keys]
// detach, which named the same key; a missing file or key gives the
// default.
func prefixKey() (chord, error) {
	def, _ := parseChord(DefaultPrefixKey)
	path, err := config.Path()
	if err != nil {
		return def, nil
	}
	var cfg struct {
		Keys struct {
			Prefix string `toml:"prefix"`
			Detach string `toml:"detach"`
		} `toml:"keys"`
	}
	if _, err := toml.DecodeFile(path, &cfg); errors.Is(err, fs.ErrNotExist) {
		return def, nil
	} else if err != nil {
		return def, fmt.Errorf("%s: %w", path, err)
	}
	key := cmp.Or(cfg.Keys.Prefix, cfg.Keys.Detach)
	if key == "" {
		return def, nil
	}
	c, err := parseChord(key)
	if err != nil {
		return def, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// ConfigPrefix is the prefix key config.toml sets, for the dashboard;
// errors are reported by the attach client, which reads the same key.
func ConfigPrefix() string {
	c, _ := prefixKey()
	return c.String()
}

// prefixCommands are the dashboard's keys that work after the prefix in a
// session, as on the dashboard. Those of popupCommands open their popup
// over the session (Over); p ] [ return to the dashboard and run there.
var prefixCommands = map[string]bool{"a": true, "p": true, "]": true, "[": true, "i": true, "t": true, ",": true, "?": true}

// popupCommands are the prefix commands that open a popup over the
// session: the project popup, the inbox, the tasks, the settings and the
// help.
var popupCommands = map[string]bool{"a": true, "i": true, "t": true, ",": true, "?": true}

var specialKeys = map[rune]emu.SpecialKey{
	uv.KeyEnter: emu.KeyEnter, uv.KeyTab: emu.KeyTab,
	uv.KeyBackspace: emu.KeyBackspace, uv.KeyEscape: emu.KeyEscape,
	uv.KeySpace: emu.KeySpace,
	uv.KeyUp:    emu.KeyUp, uv.KeyDown: emu.KeyDown,
	uv.KeyLeft: emu.KeyLeft, uv.KeyRight: emu.KeyRight,
	uv.KeyHome: emu.KeyHome, uv.KeyEnd: emu.KeyEnd,
	uv.KeyPgUp: emu.KeyPageUp, uv.KeyPgDown: emu.KeyPageDown,
	uv.KeyInsert: emu.KeyInsert, uv.KeyDelete: emu.KeyDelete,
	uv.KeyKpEnter: emu.KeyKpEnter,
	uv.KeyF1:      emu.KeyF1, uv.KeyF2: emu.KeyF2, uv.KeyF3: emu.KeyF3,
	uv.KeyF4: emu.KeyF4, uv.KeyF5: emu.KeyF5, uv.KeyF6: emu.KeyF6,
	uv.KeyF7: emu.KeyF7, uv.KeyF8: emu.KeyF8, uv.KeyF9: emu.KeyF9,
	uv.KeyF10: emu.KeyF10, uv.KeyF11: emu.KeyF11, uv.KeyF12: emu.KeyF12,
}

func mods(m uv.KeyMod) emu.Mods {
	var out emu.Mods
	if m.Contains(uv.ModShift) {
		out |= emu.ModShift
	}
	if m.Contains(uv.ModCtrl) {
		out |= emu.ModCtrl
	}
	if m.Contains(uv.ModAlt) || m.Contains(uv.ModMeta) {
		out |= emu.ModAlt
	}
	if m.Contains(uv.ModSuper) {
		out |= emu.ModSuper
	}
	return out
}

// toKey translates a key decoded from the outer terminal; ok is false for
// keys the pane can't receive.
func toKey(k uv.Key) (emu.Key, bool) {
	out := emu.Key{Mods: mods(k.Mod), Text: k.Text}
	if sk, ok := specialKeys[k.Code]; ok {
		out.Special = sk
		return out, true
	}
	if k.Code >= uv.KeyExtended {
		return out, false
	}
	out.Rune = k.Code
	return out, true
}

var mouseButtons = map[uv.MouseButton]emu.MouseButton{
	uv.MouseLeft: emu.MouseLeft, uv.MouseMiddle: emu.MouseMiddle, uv.MouseRight: emu.MouseRight,
	uv.MouseWheelUp: emu.MouseWheelUp, uv.MouseWheelDown: emu.MouseWheelDown,
	uv.MouseWheelLeft: emu.MouseWheelLeft, uv.MouseWheelRight: emu.MouseWheelRight,
}

// toMouse translates a mouse event; X and Y stay in outer cells.
func toMouse(ev uv.Event) (emu.Mouse, bool) {
	var m uv.Mouse
	action := emu.MousePress
	switch e := ev.(type) {
	case uv.MouseClickEvent:
		m = uv.Mouse(e)
	case uv.MouseReleaseEvent:
		m, action = uv.Mouse(e), emu.MouseRelease
	case uv.MouseMotionEvent:
		m, action = uv.Mouse(e), emu.MouseMotion
	case uv.MouseWheelEvent:
		m = uv.Mouse(e)
	default:
		return emu.Mouse{}, false
	}
	return emu.Mouse{Action: action, Button: mouseButtons[m.Button], Mods: mods(m.Mod), X: m.X, Y: m.Y}, true
}
