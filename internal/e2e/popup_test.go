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

	"github.com/theclifmeister/termalator/internal/emu"
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
	Golden(t, w.Screen(), "popup-overview.txt", popupMasks...)

	w.Type("3")
	w.WaitFor("✓ Draft", wait)
	w.WaitFor("ON DECK", wait)
	screens = append(screens, w.Screen())
	Golden(t, w.Screen(), "popup-tasks.txt", popupMasks...)
	w.Key(emu.Key{Special: emu.KeyTab})
	w.WaitFor("Start threads", wait)
	Golden(t, w.Screen(), "popup-settings.txt", popupMasks...)

	// Start threads: ask first → automatically, saved and in the
	// coordinator's context.
	w.Key(Enter)
	w.WaitFor("automatically", wait)
	if out := env.MustCLI("context", "--project", slug); !strings.Contains(out, "start_threads=auto") {
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
	if out := env.MustCLI("context", "--project", slug); !strings.Contains(out, "parallel_threads=9 · auto_close=8 days after done or merged") {
		t.Fatalf("context after the numbers:\n%s", out)
	}
	// Remote control, the last row: on, and saved.
	for range 2 {
		w.Key(keyDown)
	}
	w.Key(Enter)
	w.WaitUntil("remote control on", wait, func(sc string) bool {
		return regexp.MustCompile(`Remote control\s+on`).MatchString(sc)
	})
	data, err := os.ReadFile(filepath.Join(env.Home, "config.toml"))
	if err != nil || !strings.Contains(string(data), "coordinator_remote_control = true") || !strings.Contains(string(data), `start_threads = "auto"`) ||
		!strings.Contains(string(data), "parallel_threads = 9\nauto_close = \"days\"\nauto_close_days = 8\n") {
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
	w.WaitFor("any other key returns", wait)
	help := popupBody(w.Screen(), cols)
	screens = append(screens, w.Screen())
	// Each wraps to its own box's width and shows what fits: word for
	// word, the shorter is where the longer starts.
	kw, hw := strings.Fields(strings.Join(keys[min(2, len(keys)):], " ")), strings.Fields(strings.Join(help, " "))
	if n := min(len(kw), len(hw)); n < 20 || !slices.Equal(kw[:n], hw[:n]) {
		t.Fatalf("keys tab and help differ:\n%s\n----\n%s", strings.Join(keys, "\n"), strings.Join(help, "\n"))
	}
	w.Type("x")
	w.WaitUntil("help closed", wait, func(sc string) bool { return !strings.Contains(sc, "any other key returns") })

	// , has the settings of every project.
	w.Type(",")
	w.WaitFor("Prefix key", wait)
	w.WaitFor("Default agent", wait)
	screens = append(screens, w.Screen())
	w.Key(keyEsc)

	// prefix+a in a session: back to the dashboard with the popup open,
	// on this console only.
	w2 := env.Window(cols, 40)
	w2.WaitFor("SESSIONS", wait)
	w.Type("s")
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, "prefix+d dashboard") })
	w.Prefix("a")
	w.WaitFor("1 Overview", wait)
	w2.WaitFor("SESSIONS", wait)
	w2.Quiet(300 * time.Millisecond)
	if strings.Contains(w2.Screen(), "1 Overview") {
		t.Fatalf("the popup opened on the other console too:\n%s", w2.Screen())
	}
	screens = append(screens, w.Screen())
	for _, sc := range screens {
		low := strings.ToLower(sc)
		for _, bad := range []string{"config", "toml", "coordinator_remote_control", "start_threads"} {
			if strings.Contains(low, bad) {
				t.Errorf("a screen names %q:\n%s", bad, sc)
			}
		}
	}
	w.Key(keyEsc)
	w.Type("q")
	w.WaitExit(wait)
	w2.Type("q")
	w2.WaitExit(wait)
}
