//go:build unix

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

	"github.com/theclifmeister/terminatr/internal/emu"
	"github.com/theclifmeister/terminatr/internal/project"
	"github.com/theclifmeister/terminatr/internal/thread"
)

// popupBody is the text inside the popup's box: the lines between its
// side borders. The box is centred on the whole window, so it starts
// where its top border does.
func popupBody(screen string, cols int) []string {
	lines := strings.Split(screen, "\n")
	x := 0
	for _, l := range lines {
		if i := strings.Index(l, "╭─"); i >= 0 {
			x = len([]rune(l[:i]))
			break
		}
	}
	var out []string
	for _, l := range lines {
		r := []rune(l)
		s := string(r[min(x, len(r)):])
		if !strings.HasPrefix(s, "│ ") {
			continue
		}
		j := strings.Index(s[len("│ "):], " │")
		if j < 0 {
			continue
		}
		out = append(out, strings.TrimRight(s[len("│ "):len("│ ")+j], " "))
	}
	return out
}

// liveRows counts the rows of pane (a PaneScreen with a popup open)
// that start with text: the session's output showing above and below
// the box, which is centred on the whole window and so covers the left
// edge of the pane beside it.
func liveRows(t *testing.T, pane, text string) int {
	t.Helper()
	n := 0
	for _, l := range strings.Split(pane, "\n") {
		if strings.HasPrefix(l, text) {
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

// saved waits until cond holds and the dashboard has saved the change:
// a setting shows its new value at once, with "working…" until the save
// is done, and the settings ignore enter, + and - till then.
func saved(w *Window, desc string, cond func(screen string) bool) string {
	w.env.T.Helper()
	return w.WaitUntil(desc+", saved", wait, func(sc string) bool { return cond(sc) && !lastLine(sc, "working…") })
}

// TestSmokeProjectPopup: a opens the project popup; its tabs switch;
// a setting changes in place, persists and reaches the coordinator's
// context; the Keys tab is the help's list; the Memory tab shows the
// project's memory; prefix+a opens it from a
// session, on this console only; no screen names the settings file.
func TestSmokeProjectPopup(t *testing.T) {
	env := New(t)
	slug, _ := newProject(env, "demo")
	repo := env.Workdir()
	env.MustCLI("project", "repo", "add", repo, "--project", slug)
	env.MustCLI("task", "add", "Write the README", "--project", slug, "--step", "Draft", "--step", "Review")
	env.MustCLI("task", "steps", "T1", "check", "1", "--project", slug)
	env.MustCLI("task", "status", "T1", "started", "--project", slug)
	env.MustCLI("task", "add", "Ship it", "--status", "ready", "--project", slug)

	// A resolved thread with two library files, for the Library tab.
	t.Setenv("TERMINATR_HOME", env.Home)
	p, err := project.Open(slug)
	if err != nil {
		t.Fatal(err)
	}
	done, err := thread.Create(p, thread.Record{Title: "Research the options", Task: "T1", State: thread.Resolved})
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(thread.Path(p, done.ID, "library"), 0o755)
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for name, text := range map[string]string{"options.md": "# Options\n\nUse the first one.\n", "data.json": `{"a":[1,2]}`} {
		f := thread.Path(p, done.ID, "library", name)
		os.WriteFile(f, []byte(text), 0o644)
		os.Chtimes(f, at, at)
	}

	const cols = 120
	w := env.Window(cols, 40)
	w.WaitFor("SESSIONS", wait)
	var screens []string
	w.Type("a")
	w.WaitFor("1 overview", wait)
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

	// Library: the resolved thread's files by name and size, one read
	// in the popup, the rest deleted after y.
	w.Type("7")
	w.WaitFor("options.md", wait)
	w.Golden("popup-library.txt", popupMasks...)
	w.Key(Enter)
	w.WaitFor(`"a": [`, wait) // data.json is first (equal times, by name), indented
	w.Key(emu.Key{Special: emu.KeyEscape})
	w.WaitFor("options.md", wait)
	w.Type("D")
	w.WaitFor("Delete all 2 library files of thread "+done.ID, wait)
	w.Type("y")
	w.WaitFor("no files", wait)
	if out := env.MustCLI("library", "list", "--project", slug); out != "" {
		t.Fatalf("library after D:\n%s", out)
	}
	w.Type("4")

	// Start threads: ask first → automatically, saved and in the
	// coordinator's context.
	w.Key(Enter)
	saved(w, "automatically", func(sc string) bool { return strings.Contains(sc, "automatically") })
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
	saved(w, "parallel threads 9", func(sc string) bool {
		return regexp.MustCompile(`Parallel threads\s+9 · 0 working now`).MatchString(sc)
	})
	w.Key(keyDown)
	w.Key(Enter)
	saved(w, "7 days", func(sc string) bool { return strings.Contains(sc, "7 days after it finishes") })
	w.Type("+")
	saved(w, "8 days", func(sc string) bool { return strings.Contains(sc, "8 days after it finishes") })
	// Presses while a save is in flight wait their turn: none is lost.
	for range 3 {
		w.Type("+")
	}
	saved(w, "11 days", func(sc string) bool { return strings.Contains(sc, "11 days after it finishes") })
	// The popup shows a change before its write to config.toml lands.
	if out := ""; !Poll(wait, func() bool {
		out = env.MustCLI("context", "--project", slug)
		return strings.Contains(out, "parallel_threads=9 · auto_close=11 days after done or merged")
	}) {
		t.Fatalf("context after the numbers:\n%s", out)
	}
	// Complete tasks: by you → when merged, in the coordinator's context.
	w.Key(keyDown)
	w.Key(Enter)
	saved(w, "complete tasks when merged", func(sc string) bool {
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
	saved(w, "remote control on", func(sc string) bool {
		return regexp.MustCompile(`Remote control\s+on`).MatchString(sc)
	})
	data, err := os.ReadFile(filepath.Join(env.Home, "config.toml"))
	if err != nil || !strings.Contains(string(data), "coordinator_remote_control = true") || !strings.Contains(string(data), `start_threads = "auto"`) ||
		!strings.Contains(string(data), "parallel_threads = 9\nauto_close = \"days\"\nauto_close_days = 11\ncomplete_tasks = \"merged\"\n") {
		t.Fatalf("settings file (%v):\n%s", err, data)
	}
	screens = append(screens, w.Screen())

	// The Keys tab and the help show the same list.
	w.Type("5")
	w.WaitFor("On the dashboard", wait)
	keys := popupBody(w.Screen(), cols)
	screens = append(screens, w.Screen())
	// The Memory tab: the project's CONTEXT.md and MEMORY.md as text,
	// with no file path.
	w.Type("6")
	w.WaitFor("Living context for this project", wait)
	w.WaitFor("One line per memory file", wait)
	if sc := w.Screen(); strings.Contains(sc, ".md") {
		t.Fatalf("the Memory tab shows a file:\n%s", sc)
	}
	screens = append(screens, w.Screen())
	w.Key(keyEsc)
	w.WaitUntil("popup closed", wait, func(sc string) bool { return !strings.Contains(sc, "1 overview") })
	w.Type("?")
	w.WaitFor("↑ ↓ scroll · esc close", wait)
	help := popupBody(w.Screen(), cols)
	// The help leads with the prefix key, then the same keys; each box
	// ends with its own action row, which is no key line.
	for len(help) > 0 && !strings.Contains(help[0], "On the dashboard") {
		help = help[1:]
	}
	help = help[:max(len(help)-1, 0)]
	keys = keys[:max(len(keys)-1, 0)]
	screens = append(screens, w.Screen())
	// Each wraps to its own box's width and shows what fits: word for
	// word, the shorter is where the longer starts.
	kw, hw := strings.Fields(strings.Join(keys[min(2, len(keys)):], " ")), strings.Fields(strings.Join(help, " "))
	if n := min(len(kw), len(hw)); n < 20 || !slices.Equal(kw[:n], hw[:n]) {
		t.Fatalf("keys tab and help differ:\n%s\n----\n%s", strings.Join(keys, "\n"), strings.Join(help, "\n"))
	}
	w.Key(keyEsc)
	w.WaitUntil("help closed", wait, func(sc string) bool { return !strings.Contains(sc, "↑ ↓ scroll · esc close") })

	// , has the settings of every project.
	w.Type(",")
	w.WaitFor("Prefix key", wait)
	w.WaitFor("Details panel", wait)
	screens = append(screens, w.Screen())
	w.Key(keyEsc)

	// prefix+a in a session: the popup opens over the session, on this
	// console only; the other stays on the session, and esc goes back to
	// it.
	w2 := env.Window(cols, 40)
	w2.WaitFor("SESSIONS", wait)
	id := env.StartShell("/")
	w.OpenSession(id)
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, "prefix+d dashboard") })
	w2.WaitUntil("w2 attached", wait, func(sc string) bool { return lastLine(sc, "prefix+d dashboard") })
	w.Prefix("a")
	w.WaitFor("1 overview", wait)
	if sc := w.Screen(); strings.Contains(sc, "SESSIONS") || !strings.Contains(sc, "tm s-") {
		t.Fatalf("the popup isn't over the session:\n%s", sc)
	}
	w2.Quiet(300 * time.Millisecond)
	if sc := w2.Screen(); strings.Contains(sc, "1 overview") || !lastLine(sc, "prefix+d dashboard") {
		t.Fatalf("the other console left the session:\n%s", sc)
	}
	screens = append(screens, w.Screen())
	// The session keeps drawing under the popup: the other console types
	// into it, and this one shows the output with the popup still open.
	// The pane shows above and below the box, which is centred on the
	// window. The output outruns the pane's height, so every row is live
	// output, whatever the shell's prompt takes: the strips above and
	// below the box (4 rows, and 2 over the status bar) are all output.
	w2.Type("for i in $(seq 100); do echo live-$i; done\r")
	w.WaitUntil("output under the popup", wait, func(string) bool {
		return liveRows(t, w.PaneScreen(), "live-") >= 3 && strings.Contains(w.Screen(), "1 overview")
	})
	w.Key(keyEsc)
	w.WaitUntil("back on the session", wait, func(sc string) bool {
		return lastLine(sc, "prefix+d dashboard") && !strings.Contains(sc, "1 overview")
	})
	// prefix+? the same; prefix+d from the popup goes to the dashboard.
	w.Prefix("?")
	w.WaitFor("↑ ↓ scroll · esc close", wait)
	w.Prefix("d")
	w.WaitFor("SESSIONS", wait)
	w2.WaitFor("SESSIONS", wait)
	if strings.Contains(w.Screen(), "↑ ↓ scroll · esc close") {
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
