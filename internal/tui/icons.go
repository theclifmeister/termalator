package tui

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"sync/atomic"

	"github.com/BurntSushi/toml"

	"github.com/theclifmeister/termilator/internal/config"
)

// Icon sets (docs/SPEC.md §4): the glyphs of the sidebar's tree and of
// the states, todos and progress bars wherever tm draws them. The global
// setting picks one of auto, nerd, unicode or ascii; auto is decided by
// each console for itself, since the font belongs to the terminal you
// look at: Nerd Font icons in Ghostty, which bundles the Nerd Font
// symbols, plain Unicode elsewhere. Every glyph is one cell wide (two for
// a connector), so columns line up in each set (TestIconWidths); none is
// an emoji, whose width differs between terminals.

// The values of the icons setting.
const (
	IconsAuto    = "auto"
	IconsNerd    = "nerd"
	IconsUnicode = "unicode"
	IconsASCII   = "ascii"
)

// IconChoices are the icons setting's values, in the order enter steps
// through them.
var IconChoices = []string{IconsAuto, IconsNerd, IconsUnicode, IconsASCII}

// iconSet is one set of glyphs.
type iconSet struct {
	name string
	// The tree: a project's folder (open: the current one), the
	// connectors of the rows under it (two cells), and the marks before
	// the coordinator's and a thread's label ("" for none).
	folder, folderOpen string
	mid, end           string
	coord, thread      string
	// hint marks a project one of whose threads is blocked or waiting, or
	// one of whose tasks needs you;
	// current marks the current project in the slim strip; remote follows
	// a coordinator with remote control on.
	hint, current, remote string
	// The states (stateLook): none is a coordinator that doesn't run,
	// other a stopped, exited or resolved one.
	working, blocked, idle, starting, running, review, done, other, none string
	// Todos and steps (todoGlyph), and the progress bar's cells (bar).
	todoDone, todoNow, todoOpen string
	barOn, barOff               string
}

var unicodeIcons = iconSet{
	name:   IconsUnicode,
	folder: "■", folderOpen: "■",
	mid: "├─", end: "└─",
	hint: "◆", current: "▸", remote: "⌁",
	working: "●", blocked: "▲", idle: "○", starting: "◌", running: "●", review: "◆", done: "✓", other: "·", none: "·",
	todoDone: "✓", todoNow: "◐", todoOpen: "○",
	barOn: "▰", barOff: "▱",
}

// nerdIcons are Nerd Font glyphs (Private Use Area), with Unicode where
// a plain shape reads as well.
var nerdIcons = iconSet{
	name:   IconsNerd,
	folder: "", folderOpen: "", // nf-fa-folder, folder_open
	mid: "├╴", end: "└╴",
	coord: "\U000f06a9", thread: "", // nf-md-robot, nf-oct-git_branch
	hint: "", current: "", remote: "", // bell, chevron_right, wifi
	working: "", blocked: "", idle: "", starting: "", running: "",
	review: "", done: "", other: "", none: "·", // circle, warning, circle_o, spinner, eye, check, stop_circle
	todoDone: "", todoNow: "", todoOpen: "", // check, dot_circle_o, circle_o
	barOn: "▰", barOff: "▱",
}

var asciiIcons = iconSet{
	name:   IconsASCII,
	folder: "+", folderOpen: "+",
	mid: "|-", end: "`-",
	hint: "?", current: ">", remote: "@",
	working: "*", blocked: "!", idle: "o", starting: "~", running: "*", review: "#", done: "v", other: "-", none: ".",
	todoDone: "x", todoNow: "~", todoOpen: "o",
	barOn: "#", barOff: "-",
}

// icons is this console's set, iconsValue the setting's value it came
// from.
var (
	icons      atomic.Pointer[iconSet]
	iconsValue atomic.Value
)

func init() { icons.Store(&unicodeIcons); iconsValue.Store(IconsUnicode) }

// ic is this console's icon set.
func ic() *iconSet { return icons.Load() }

// iconSetFor is the set a setting's value picks in a terminal whose
// TERM_PROGRAM is term.
func iconSetFor(value, term string) *iconSet {
	switch value {
	case IconsNerd:
		return &nerdIcons
	case IconsASCII:
		return &asciiIcons
	case IconsUnicode:
		return &unicodeIcons
	}
	if term == "ghostty" {
		return &nerdIcons
	}
	return &unicodeIcons
}

// setIcons makes the setting's value this console's icon set.
func setIcons(value string) {
	icons.Store(iconSetFor(value, os.Getenv("TERM_PROGRAM")))
	iconsValue.Store(value)
}

// iconsSetting is the icons setting's value this console uses.
func iconsSetting() string { return iconsValue.Load().(string) }

// configIcons reads [ui] icons from config.toml: auto when the file or
// the key is missing or unreadable (tm doctor reports a broken file).
func configIcons() string {
	path, err := config.Path()
	if err != nil {
		return IconsAuto
	}
	var cfg struct {
		UI struct {
			Icons string `toml:"icons"`
		} `toml:"ui"`
	}
	if _, err := toml.DecodeFile(path, &cfg); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return IconsAuto
	}
	if !slices.Contains(IconChoices, cfg.UI.Icons) {
		return IconsAuto
	}
	return cfg.UI.Icons
}

// loadIcons makes config.toml's icons setting this console's set.
func loadIcons() string {
	v := configIcons()
	setIcons(v)
	return v
}
