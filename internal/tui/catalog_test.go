package tui

import (
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/terminatr/internal/agent"
	"github.com/theclifmeister/terminatr/internal/config"
)

// typeLine replaces an input's text with s and submits it.
func typeLine(m *dash, s string) tea.Cmd {
	m.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	return cmd
}

// catalogOf is claude's catalog as saved: its names, the default
// starred.
func catalogOf(t *testing.T) string {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := agent.Load("")
	a, _ := reg.Get("claude")
	var out []string
	for _, x := range agent.Models(a, cfg) {
		n := x.Name
		if x.Default {
			n += "*"
		}
		out = append(out, n)
	}
	return strings.Join(out, " ")
}

// TestCatalogPage: General's Models row opens each agent's models as
// released; adding, changing, making default and removing one saves
// the agent's own list, which the page and the row show, and r goes
// back to the list as released.
func TestCatalogPage(t *testing.T) {
	t.Setenv("TERMINATR_HOME", t.TempDir())
	src := &fakeSource{data: testData()}
	m := newDash(DashOptions{Source: src, Width: 120, Height: 80, State: DashState{Current: "beta"}})
	m.setData(src.data)
	press(m, ",")
	modsRow(t, m, "Models")
	sv := m.top().(*settingsView)
	if got := sv.tabs[0].rows[sv.tabs[0].sel].value(m); got != "claude 3" {
		t.Fatalf("row value %q", got)
	}
	keyPress(m, "enter")
	v, ok := m.top().(*catalogView)
	if !ok {
		t.Fatalf("enter opened %T", m.top())
	}
	out := screen(m)
	for _, want := range []string{"Models", "claude", "default the agent's own · as released", "opus", "most capable", "haiku", "a add"} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q:\n%s", want, out)
		}
	}

	// Add on the agent's line: name, then about.
	keyPress(m, "a")
	typeLine(m, "opus-6")
	run(m, typeLine(m, "newest, for the hardest work"))
	if got := catalogOf(t); got != "opus sonnet haiku opus-6" {
		t.Fatalf("after add: %q", got)
	}
	if !strings.Contains(screen(m), "your list (r: as released)") {
		t.Fatalf("page after add:\n%s", screen(m))
	}

	// A name already listed, or not one word, is refused.
	keyPress(m, "a")
	typeLine(m, "sonnet")
	if !strings.Contains(v.err, "already lists sonnet") {
		t.Fatalf("duplicate: %q", v.err)
	}
	keyPress(m, "a")
	typeLine(m, "two words")
	if v.err == "" || m.top() != overlay(v) {
		t.Fatalf("two words: %q %T", v.err, m.top())
	}

	// Make opus-6 the default, then rename it: the default follows.
	for range 4 {
		keyPress(m, "down")
	}
	run(m, keyPress(m, "*"))
	if got := catalogOf(t); got != "opus sonnet haiku opus-6*" {
		t.Fatalf("after default: %q", got)
	}
	keyPress(m, "enter")
	typeLine(m, "opus-6.1")
	run(m, typeLine(m, "newest"))
	if got := catalogOf(t); got != "opus sonnet haiku opus-6.1*" {
		t.Fatalf("after rename: %q", got)
	}
	cfg, _ := config.Load()
	if s := cfg.Agent("claude"); s.Models[3].About != "newest" || s.DefaultModel != "opus-6.1" {
		t.Fatalf("saved %+v", s)
	}

	// Remove haiku, then the default: none is left.
	keyPress(m, "up")
	run(m, keyPress(m, "d"))
	if got := catalogOf(t); got != "opus sonnet opus-6.1*" {
		t.Fatalf("after remove: %q", got)
	}
	v.sel = 3
	run(m, keyPress(m, "d"))
	if got := catalogOf(t); got != "opus sonnet" {
		t.Fatalf("after removing the default: %q", got)
	}
	if got := catalogWords(m.catalogs); got != "claude 2" {
		t.Fatalf("row value %q", got)
	}

	// r: as released again, both keys gone.
	run(m, keyPress(m, "r"))
	if got := catalogOf(t); got != "opus sonnet haiku" {
		t.Fatalf("after reset: %q", got)
	}
	path, _ := config.Path()
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "models") || strings.Contains(string(data), "default_model") {
		t.Fatalf("file after reset:\n%s", data)
	}
	keyPress(m, "esc")
	if _, ok := m.top().(*settingsView); !ok {
		t.Fatalf("esc left %T", m.top())
	}
}

// TestModelsSettingStale: an allowed model no catalog lists is shown
// stale, not dropped; enter leaves it out, and the rest stays.
func TestModelsSettingStale(t *testing.T) {
	src, m := popupData(t)
	if err := config.SetDefaults("models", []string{"opus", "gone"}); err != nil {
		t.Fatal(err)
	}
	src.settings = append(src.settings, "defaults.models") // Load reads the file
	m.setData(src.Load())
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	press(m, ",")
	keyPress(m, "2")
	sv := m.top().(*settingsView)
	i := slices.IndexFunc(sv.tabs[1].rows, func(r setting) bool { return r.label == "Thread models" })
	sv.tabs[1].sel = i
	keyPress(m, "enter")
	mv, ok := m.top().(*modelsView)
	if !ok {
		t.Fatalf("enter opened %T", m.top())
	}
	out := screen(m)
	if !strings.Contains(out, "gone") || !strings.Contains(out, "stale: no agent lists it") {
		t.Fatalf("no stale row:\n%s", out)
	}
	for range 3 {
		keyPress(m, "down")
	}
	run(m, keyPress(m, "enter"))
	cfg, _ := config.Load()
	if s, _ := cfg.AllProjects(); !slices.Equal(s.Models, []string{"opus"}) {
		t.Fatalf("saved %v", s.Models)
	}
	if slices.Contains(mv.rows(), "gone") {
		t.Fatalf("rows %v", mv.rows())
	}
}
