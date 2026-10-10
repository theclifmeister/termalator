package tui

import (
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/theclifmeister/terminatr/internal/config"
	"github.com/theclifmeister/terminatr/internal/models"
	"github.com/theclifmeister/terminatr/internal/models/modelstest"
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

// saved is claude's settings as saved: hidden, added, default.
func saved(t *testing.T) config.AgentSettings {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Agent("claude")
}

// TestCatalogPage: General's Models row opens each installed agent's
// models as the agent answered; * makes one the default, h hides one,
// a adds one the agent doesn't list and d removes it again, u offers a
// refused one again, R asks the agent again and r goes back to what the
// agent lists. Each change is saved at once.
func TestCatalogPage(t *testing.T) {
	t.Setenv("TERMINATR_HOME", t.TempDir())
	modelstest.Answer(t, "claude", "alpha", "beta", "old-1~")
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
	for _, want := range []string{"Models", "claude", "3 models · asked 1.0 today (test) · default the agent's own", "alpha", "about alpha", "older", "R ask again"} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q:\n%s", want, out)
		}
	}

	// Add one the agent doesn't list; one it offers, or not one word,
	// is refused.
	keyPress(m, "a")
	run(m, typeLine(m, "mine-1"))
	if got := saved(t); !slices.Equal(got.Add, []string{"mine-1"}) {
		t.Fatalf("after add: %+v", got)
	}
	if !strings.Contains(screen(m), "yours, claude doesn't list it") {
		t.Fatalf("page after add:\n%s", screen(m))
	}
	keyPress(m, "a")
	typeLine(m, "alpha")
	if !strings.Contains(v.err, "already offers alpha") {
		t.Fatalf("duplicate: %q", v.err)
	}
	keyPress(m, "a")
	typeLine(m, "two words")
	if v.err == "" || m.top() != overlay(v) {
		t.Fatalf("two words: %q %T", v.err, m.top())
	}

	// The default, on and off again.
	v.sel = 1 // alpha
	run(m, keyPress(m, "*"))
	if got := saved(t); got.DefaultModel != "alpha" {
		t.Fatalf("after default: %+v", got)
	}
	run(m, keyPress(m, "*"))
	if got := saved(t); got.DefaultModel != "" {
		t.Fatalf("after default off: %+v", got)
	}

	// Hide beta; d on it only says h hides it.
	v.sel = 2
	keyPress(m, "d")
	if !strings.Contains(m.msg, "h hides it") {
		t.Fatalf("d on a listed model: %q", m.msg)
	}
	run(m, keyPress(m, "h"))
	if got := saved(t); !slices.Equal(got.Hide, []string{"beta"}) {
		t.Fatalf("after hide: %+v", got)
	}
	if got := catalogWords(m.catalogs); got != "claude 3" {
		t.Fatalf("row value %q", got) // alpha, old-1, mine-1
	}
	// Remove mine-1, the one added.
	v.sel = slices.IndexFunc(v.items(), func(it catalogItem) bool { x, ok := v.model(it); return ok && x.Name == "mine-1" })
	run(m, keyPress(m, "d"))
	if got := saved(t); len(got.Add) != 0 {
		t.Fatalf("after remove: %+v", got)
	}

	// A refused model shows why; u offers it again.
	if err := models.MarkRefused("claude", "alpha", "no access"); err != nil {
		t.Fatal(err)
	}
	v.reload(m)
	if !strings.Contains(screen(m), "refused for your account: no access") {
		t.Fatalf("refused:\n%s", screen(m))
	}
	v.sel = slices.IndexFunc(v.items(), func(it catalogItem) bool { x, ok := v.model(it); return ok && x.Name == "alpha" })
	run(m, keyPress(m, "u"))
	if c, _ := models.Load("claude"); len(c.Refused) != 0 {
		t.Fatalf("still refused: %+v", c.Refused)
	}

	// R asks again; r goes back to what the agent lists.
	run(m, keyPress(m, "R"))
	if !slices.Contains(src.settings, "refresh claude") {
		t.Fatalf("no refresh: %v", src.settings)
	}
	run(m, keyPress(m, "r"))
	if got := saved(t); !got.Empty() {
		t.Fatalf("after reset: %+v", got)
	}
	path, _ := config.Path()
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "hide") || strings.Contains(string(data), "default_model") {
		t.Fatalf("file after reset:\n%s", data)
	}

	// Logged out: the models are unknown, and nothing can be added.
	modelstest.LoggedOut(t, "claude")
	v.reload(m)
	if out := screen(m); !strings.Contains(out, "unknown: logged out of claude") || !strings.Contains(out, "--model is refused") {
		t.Fatalf("logged out:\n%s", out)
	}
	v.sel = 0
	keyPress(m, "a")
	if !strings.Contains(v.err, "unknown") {
		t.Fatalf("add while unknown: %q", v.err)
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
	modelstest.Answer(t, "claude", "alpha", "beta", "gamma")
	if err := config.SetDefaults("models", []string{"alpha", "gone"}); err != nil {
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
	if s, _ := cfg.AllProjects(); !slices.Equal(s.Models, []string{"alpha"}) {
		t.Fatalf("saved %v", s.Models)
	}
	if slices.Contains(mv.rows(), "gone") {
		t.Fatalf("rows %v", mv.rows())
	}
}
