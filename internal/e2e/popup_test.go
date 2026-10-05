package e2e

// The project popup (a, prefix+a) and the settings (docs/SPEC.md §4,
// §11.2).

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/theclifmeister/termilator/internal/emu"
)

// popupBody is the text inside the popup's box: the lines between its
// side borders, right of the sidebar.
func popupBody(screen string, cols int) []string {
	var out []string
	for _, l := range strings.Split(screen, "\n") {
		r := []rune(l)
		r = r[min(SideCols(cols), len(r)):]
		s := string(r)
		i, j := strings.Index(s, "│ "), strings.LastIndex(s, " │")
		if i < 0 || j <= i {
			continue
		}
		out = append(out, strings.TrimRight(s[i+len("│ "):j], " "))
	}
	return out
}

// liveRows counts the rows of pane (a PaneScreen with a popup open)
// whose strip left of the popup's box starts with as much of text as
// fits there: the session's output showing under the popup, at any
// sidebar or popup width.
func liveRows(t *testing.T, pane, text string) int {
	t.Helper()
	lines := strings.Split(pane, "\n")
	edge := -1 // the box's left border, in pane columns
	for _, l := range lines {
		if i := strings.Index(l, "╭─"); i >= 0 {
			edge = len([]rune(l[:i]))
			break
		}
	}
	if edge < 0 {
		return 0 // no popup yet
	}
	if edge == 0 {
		t.Fatal("the popup covers the whole pane: nothing of it shows to check")
	}
	want := string([]rune(text)[:min(edge, len([]rune(text)))])
	n := 0
	for _, l := range lines {
		if r := []rune(l); len(r) >= edge && strings.HasPrefix(string(r[:edge]), want) {
			n++
		}
	}
	return n
}

// popupMasks hide the temp paths and the machine's name, with the
// padding after them, whose width depends on theirs.
var popupMasks = []Mask{
	{"tmp", regexp.MustCompile(`(/\S*/T/\S*|/tmp/\S*) *`)},
	{"host", regexp.MustCompile(`this one  \S+ *`)},
}

// TestSmokeProjectPopup: a opens the project popup; its tabs switch;
// a setting changes in place, persists and reaches the coordinator's
// context; the Keys tab is the help's list; prefix+a opens it from a
// session, on this console only; no screen names the settings file.
func TestSmokeProjectPopup(t *testing.T) {
	env := New(t)
	slug, _ := newProject(env, "Demo")
	repo := env.Workdir()
	env.MustCLI("project", "repo", "add", repo, "--project", slug)
	env.MustCLI("task", "add", "Write the README", "--project", slug, "--step", "Draft", "--step", "Review")
	env.MustCLI("task", "steps", "T1", "check", "1", "--project", slug)
	env.MustCLI("task", "status", "T1", "started", "--project", slug)
	env.MustCLI("task", "add", "Ship it", "--project", slug)

	const cols = 120
	w := env.Window(cols, 40)
	w.WaitFor("SESSIONS", wait)
	var screens []string
	w.Type("a")
	w.WaitFor("1 Overview", wait)
	w.WaitFor("Repositories", wait)
	screens = append(screens, w.Screen())
	w.Golden("popup-overview.txt", popupMasks...)

	w.Type("3")
	w.WaitFor("✓ Draft", wait)
	w.WaitFor("ON DECK", wait)
	screens = append(screens, w.Screen())
	w.Golden("popup-tasks.txt", popupMasks...)
	w.Key(emu.Key{Special: emu.KeyRight})
	w.WaitFor("Start threads", wait)
	w.Golden("popup-settings.txt", popupMasks...)

	// Start threads: ask first → automatically, saved and in the
	// coordinator's context.
	w.Key(Enter)
	w.WaitFor("automatically", wait)
	// The popup shows a change before its write to config.toml lands.
	if out := ""; !Poll(wait, func() bool {
		out = env.MustCLI("context", "--project", slug)
		return strings.Contains(out, "start_threads=auto")
	}) {
		t.Fatalf("context after the toggle:\n%s", out)
	}
	// Parallel threads: - takes it to 9, with the working count beside
	// it. Auto-close: enter goes to days after it finishes, + adds one.
	for range 3 {
		w.Key(keyDown)
	}
	w.Type("-")
	w.WaitUntil("parallel threads 9", wait, func(sc string) bool {
		return regexp.MustCompile(`Parallel threads\s+9 · 0 working now`).MatchString(sc)
	})
	w.Key(keyDown)
	w.Key(Enter)
	w.WaitFor("7 days after it finishes", wait)
	w.Type("+")
	w.WaitFor("8 days after it finishes", wait)
	// The popup shows a change before its write to config.toml lands.
	if out := ""; !Poll(wait, func() bool {
		out = env.MustCLI("context", "--project", slug)
		return strings.Contains(out, "parallel_threads=9 · auto_close=8 days after done or merged")
	}) {
		t.Fatalf("context after the numbers:\n%s", out)
	}
	// Complete tasks: by you → when merged, in the coordinator's context.
	w.Key(keyDown)
	w.Key(Enter)
	w.WaitUntil("complete tasks when merged", wait, func(sc string) bool {
		return regexp.MustCompile(`Complete tasks\s+when merged`).MatchString(sc)
	})
	// The popup shows a change before its write to config.toml lands.
	if out := ""; !Poll(wait, func() bool {
		out = env.MustCLI("context", "--project", slug)
		return strings.Contains(out, "complete_tasks=merged")
	}) {
		t.Fatalf("context after complete tasks:\n%s", out)
	}
	// Remote control: on, and saved.
	for range 2 {
		w.Key(keyDown)
	}
	w.Key(Enter)
	w.WaitUntil("remote control on", wait, func(sc string) bool {
		return regexp.MustCompile(`Remote control\s+on`).MatchString(sc)
	})
	data, err := os.ReadFile(filepath.Join(env.Home, "config.toml"))
	if err != nil || !strings.Contains(string(data), "coordinator_remote_control = true") || !strings.Contains(string(data), `start_threads = "auto"`) ||
		!strings.Contains(string(data), "parallel_threads = 9\nauto_close = \"days\"\nauto_close_days = 8\ncomplete_tasks = \"merged\"\n") {
		t.Fatalf("settings file (%v):\n%s", err, data)
	}
	screens = append(screens, w.Screen())

	// The Keys tab and the help show the same list.
	w.Type("5")
	w.WaitFor("On the dashboard", wait)
	keys := popupBody(w.Screen(), cols)
	screens = append(screens, w.Screen())
	w.Key(keyEsc)
	w.WaitUntil("popup closed", wait, func(sc string) bool { return !strings.Contains(sc, "1 Overview") })
	w.Type("?")
	w.WaitFor("↑ ↓ scroll · esc back", wait)
	help := popupBody(w.Screen(), cols)
	screens = append(screens, w.Screen())
	// Each wraps to its own box's width and shows what fits: word for
	// word, the shorter is where the longer starts.
	kw, hw := strings.Fields(strings.Join(keys[min(2, len(keys)):], " ")), strings.Fields(strings.Join(help, " "))
	if n := min(len(kw), len(hw)); n < 20 || !slices.Equal(kw[:n], hw[:n]) {
		t.Fatalf("keys tab and help differ:\n%s\n----\n%s", strings.Join(keys, "\n"), strings.Join(help, "\n"))
	}
	w.Key(keyEsc)
	w.WaitUntil("help closed", wait, func(sc string) bool { return !strings.Contains(sc, "↑ ↓ scroll · esc back") })

	// , has the settings of every project.
	w.Type(",")
	w.WaitFor("Prefix key", wait)
	w.WaitFor("Default agent", wait)
	screens = append(screens, w.Screen())
	w.Key(keyEsc)

	// prefix+a in a session: the popup opens over the session, on this
	// console only; the other stays on the session, and esc goes back to
	// it.
	w2 := env.Window(cols, 40)
	w2.WaitFor("SESSIONS", wait)
	w.Type("s")
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, "prefix+d dashboard") })
	w2.WaitUntil("w2 attached", wait, func(sc string) bool { return lastLine(sc, "prefix+d dashboard") })
	w.Prefix("a")
	w.WaitFor("1 Overview", wait)
	if sc := w.Screen(); strings.Contains(sc, "SESSIONS") || !strings.Contains(sc, "tm s-") {
		t.Fatalf("the popup isn't over the session:\n%s", sc)
	}
	w2.Quiet(300 * time.Millisecond)
	if sc := w2.Screen(); strings.Contains(sc, "1 Overview") || !lastLine(sc, "prefix+d dashboard") {
		t.Fatalf("the other console left the session:\n%s", sc)
	}
	screens = append(screens, w.Screen())
	// The session keeps drawing under the popup: the other console types
	// into it, and this one shows the output with the popup still open.
	// Only the pane's left edge shows beside the box, as wide as the
	// sidebar and the popup leave it: each output row shows there as
	// much of "live-" as fits.
	w2.Type("for i in $(seq 30); do echo live-$i; done\r")
	w.WaitUntil("output under the popup", wait, func(string) bool {
		return liveRows(t, w.PaneScreen(), "live-") >= 10 && strings.Contains(w.Screen(), "1 Overview")
	})
	w.Key(keyEsc)
	w.WaitUntil("back on the session", wait, func(sc string) bool {
		return lastLine(sc, "prefix+d dashboard") && !strings.Contains(sc, "1 Overview")
	})
	// prefix+? the same; prefix+d from the popup goes to the dashboard.
	w.Prefix("?")
	w.WaitFor("↑ ↓ scroll · esc back", wait)
	w.Prefix("d")
	w.WaitFor("SESSIONS", wait)
	w2.WaitFor("SESSIONS", wait)
	if strings.Contains(w.Screen(), "↑ ↓ scroll · esc back") {
		t.Fatalf("prefix d left the help open:\n%s", w.Screen())
	}
	for _, sc := range screens {
		low := strings.ToLower(sc)
		for _, bad := range []string{"config", "toml", "coordinator_remote_control", "start_threads"} {
			if strings.Contains(low, bad) {
				t.Errorf("a screen names %q:\n%s", bad, sc)
			}
		}
	}
	w.Quit()
	w.WaitExit(wait)
	w2.Quit()
	w2.WaitExit(wait)
}
