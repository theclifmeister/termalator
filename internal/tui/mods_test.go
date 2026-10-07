package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/theclifmeister/terminatr/internal/config"
)

// modsRow selects the General tab's row labelled label.
func modsRow(t *testing.T, m *dash, label string) {
	t.Helper()
	sv := m.top().(*settingsView)
	for i, r := range sv.tabs[0].rows {
		if r.label == label {
			sv.tabs[0].sel = i
			return
		}
	}
	t.Fatalf("no %q setting", label)
}

// TestModsSettings: the General tab lists Mods (off) and Mods band (on)
// with plain words; enter and space change them in place, saved to the
// settings file, which keeps its comments; the band row says it needs
// Mods while that is off; no key or file name shows.
func TestModsSettings(t *testing.T) {
	t.Setenv("TERMINATR_HOME", t.TempDir())
	path, _ := config.Path()
	if err := os.WriteFile(path, []byte("# mine\n[mods]\nenabled = false # early access\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{data: testData()}
	src.data.ModsBand = true
	m := newDash(DashOptions{Source: src, Width: 120, Height: 80, State: DashState{Current: "beta"}})
	m.setData(src.data)
	press(m, ",")
	// The box scrolls: take it from the top to the bottom.
	out := screen(m)
	for range 40 {
		press(m, "down")
		out += "\n" + screen(m)
	}
	// The help text wraps to the box's width: its words, one space apart.
	words := strings.Join(strings.Fields(strings.ReplaceAll(out, "│", " ")), " ")
	for _, want := range []string{"Mods  ", "Mods band", "early access", "Claude Code 2.1.289", "after the change", "the feed still runs", "needs Mods on"} {
		if !strings.Contains(out, want) && !strings.Contains(words, want) {
			t.Errorf("settings lack %q:\n%s", want, out)
		}
	}
	for _, bad := range []string{"enabled", "[mods]", "config.toml", "band ="} {
		if strings.Contains(out, bad) {
			t.Errorf("settings name %q:\n%s", bad, out)
		}
	}

	modsRow(t, m, "Mods")
	act(m, src, "enter")
	if cfg, _ := config.Load(); !cfg.Mods || !cfg.ModsBand {
		t.Fatalf("enter: mods %v, band %v", cfg.Mods, cfg.ModsBand)
	}
	if data, _ := os.ReadFile(path); string(data) != "# mine\n[mods]\nenabled = true # early access\n" {
		t.Fatalf("file:\n%s", data)
	}
	if out := screen(m); strings.Contains(out, "needs Mods on") {
		t.Fatalf("band still says it needs Mods:\n%s", out)
	}

	modsRow(t, m, "Mods band")
	act(m, src, "space")
	if cfg, _ := config.Load(); !cfg.Mods || cfg.ModsBand {
		t.Fatalf("space: mods %v, band %v", cfg.Mods, cfg.ModsBand)
	}
	if data, _ := os.ReadFile(path); string(data) != "# mine\n[mods]\nenabled = true # early access\nband = false\n" {
		t.Fatalf("file:\n%s", data)
	}
	modsRow(t, m, "Mods")
	act(m, src, "enter")
	if cfg, _ := config.Load(); cfg.Mods || cfg.ModsBand {
		t.Fatalf("enter again: mods %v, band %v", cfg.Mods, cfg.ModsBand)
	}
}

// TestModsSettingsForm: a setting written as a dotted key is refused
// with the popup's usual message, naming no file.
func TestModsSettingsForm(t *testing.T) {
	t.Setenv("TERMINATR_HOME", t.TempDir())
	path, _ := config.Path()
	if err := os.WriteFile(path, []byte("mods.enabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 120, Height: 80, State: DashState{Current: "beta"}})
	m.setData(src.data)
	press(m, ",")
	modsRow(t, m, "Mods")
	act(m, src, "enter")
	if data, _ := os.ReadFile(path); string(data) != "mods.enabled = false\n" {
		t.Fatalf("file changed:\n%s", data)
	}
	if !strings.Contains(screen(m), "it stays as it is") {
		t.Fatalf("no refusal:\n%s", screen(m))
	}
}
