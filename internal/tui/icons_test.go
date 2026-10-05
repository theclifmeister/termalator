package tui

import (
	"reflect"
	"strings"
	"testing"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// TestIconWidths: every glyph of every set is one cell wide, a connector
// two, so the sidebar's columns line up whatever the set; none is an
// emoji. The unicode set has no Private Use Area glyph (those need a
// Nerd Font), the ascii set nothing beyond ASCII.
func TestIconWidths(t *testing.T) {
	for _, set := range []iconSet{unicodeIcons, nerdIcons, asciiIcons} {
		v := reflect.ValueOf(set)
		for i := 0; i < v.NumField(); i++ {
			name := v.Type().Field(i).Name
			g := v.Field(i).String()
			if name == "name" || g == "" && (name == "coord" || name == "thread") {
				continue
			}
			want := 1
			if name == "mid" || name == "end" {
				want = 2
			}
			if w := ansi.StringWidth(g); w != want || len([]rune(g)) != want {
				t.Errorf("%s.%s %q is %d cells, want %d", set.name, name, g, w, want)
			}
			for _, r := range g {
				pua := unicode.In(r, unicode.Co)
				switch {
				case r >= 0x1F000 && r < 0xF0000:
					t.Errorf("%s.%s %q looks like an emoji", set.name, name, g)
				case set.name == IconsUnicode && pua:
					t.Errorf("unicode.%s %q is in the Private Use Area", name, g)
				case set.name == IconsASCII && r >= 0x80:
					t.Errorf("ascii.%s %q isn't ASCII", name, g)
				}
			}
		}
	}
}

// TestIconsTree: in every set, each row of the tree is as wide as the
// sidebar and the percent and state columns line up.
func TestIconsTree(t *testing.T) {
	defer setIcons(IconsUnicode)
	d := testData()
	for _, set := range IconChoices[1:] {
		setIcons(set)
		lines := sidebarLines(buildTree(d.Projects, d.Sessions, treeIn{current: "beta"}), sideDefault, 8)
		for i, l := range lines {
			l = ansi.Strip(l)
			if w := ansi.StringWidth(l); w != sideDefault {
				t.Errorf("%s row %d %q is %d cells", set, i, l, w)
			}
			// The state column is the last but one before the border, which
			// a blank column keeps it off.
			if r := []rune(l); i > 0 && i < 7 && (r[len(r)-3] == ' ' || r[len(r)-2] != ' ') && !strings.Contains(l, "alpha") {
				t.Errorf("%s row %d %q: no state glyph", set, i, l)
			}
		}
	}
}

// TestIconsAuto: auto picks Nerd Font icons in Ghostty, Unicode elsewhere.
func TestIconsAuto(t *testing.T) {
	for _, c := range []struct{ value, term, want string }{
		{IconsAuto, "ghostty", IconsNerd}, {IconsAuto, "Apple_Terminal", IconsUnicode}, {IconsAuto, "", IconsUnicode},
		{IconsASCII, "ghostty", IconsASCII}, {IconsNerd, "", IconsNerd}, {IconsUnicode, "ghostty", IconsUnicode},
		{"bogus", "", IconsUnicode},
	} {
		if got := iconSetFor(c.value, c.term).name; got != c.want {
			t.Errorf("%s in %q: %s, want %s", c.value, c.term, got, c.want)
		}
	}
}

// TestIconsSetting: the settings popup's Icons row steps through auto,
// nerd, unicode and ascii, saves each in the settings file and changes
// this console's set at once; the next console reads it from the file.
func TestIconsSetting(t *testing.T) {
	t.Setenv("TERMILATOR_HOME", t.TempDir())
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	defer setIcons(IconsUnicode)
	if loadIcons() != IconsAuto || ic().name != IconsUnicode {
		t.Fatalf("no settings file: %s %s", iconsSetting(), ic().name)
	}
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 120, Height: 60, State: DashState{Current: "beta"}})
	m.setData(src.data)
	press(m, ",")
	if !strings.Contains(screen(m), "auto (unicode)") {
		t.Fatalf("settings:\n%s", screen(m))
	}
	sv := m.top().(*settingsView)
	for i, r := range sv.list.rows {
		if r.label == "Icons" {
			sv.list.sel = i
		}
	}
	for _, want := range []string{IconsNerd, IconsUnicode, IconsASCII, IconsAuto} {
		run(m, press(m, "enter"))
		if iconsSetting() != want || configIcons() != want {
			t.Fatalf("enter: %s, file %s, want %s (%v)", iconsSetting(), configIcons(), want, src.settings)
		}
	}
	if strings.Contains(screen(m), "icons =") || strings.Contains(screen(m), "[ui]") {
		t.Fatal("the popup names the file's keys")
	}
	for range 3 { // to ascii
		run(m, press(m, "enter"))
	}
	setIcons(IconsUnicode)
	if loadIcons() != IconsASCII || ic().name != IconsASCII {
		t.Fatalf("reloaded: %s %s", iconsSetting(), ic().name)
	}
}
