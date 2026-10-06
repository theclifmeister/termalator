package e2e

// M2 scenarios: tm attach as the user sees it (docs/SPEC.md §15 M2).
// TestSmoke* run on every PR; the rest weekly.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/theclifmeister/terminatr/internal/view"
)

// paneSize returns a session's pane size as the server reports it.
func paneSize(env *Env, s *Session) (cols, rows uint16) {
	env.T.Helper()
	for _, info := range env.Sessions() {
		if info.ID == s.ID {
			return info.Cols, info.Rows
		}
	}
	env.T.Fatalf("no session %s", s.ID)
	return 0, 0
}

func assertPaneSize(t *testing.T, env *Env, s *Session, cols, rows uint16) {
	t.Helper()
	if c, r := paneSize(env, s); c != cols || r != rows {
		t.Fatalf("pane is %d×%d, want %d×%d", c, r, cols, rows)
	}
}

// TestSmokeAttachDetachReattach is M2's first "Try it": attach while a
// program streams (the pane above the status bar), detach with Ctrl+B d, reattach from another window at
// another size, close that window mid-stream. The pane is never resized
// by attaching, nothing is lost, and every mirror equals the server.
func TestSmokeAttachDetachReattach(t *testing.T) {
	env := New(t)
	// About 6 s of output: long enough to still be streaming when the
	// second window attaches, even under the race detector.
	s := env.Start("printer", "-lines", "3000", "-delay", "2ms")

	// A window of the pane's size, the sidebar's strip and the status
	// bar: the first to show the pane, which then fills it without a
	// resize.
	w1 := env.Attach(80+uint16(SideCols(87)), 24+statusRows, s.ID)
	w1.WaitFor("line ", wait)
	// A plain session has the status bar too, with an empty row above
	// it: its id, no project or role, and the way back.
	w1.WaitUntil("the status bar", wait, func(sc string) bool {
		return lastLine(sc, " "+s.ID+" · ") && lastLine(sc, "prefix+d dashboard")
	})
	if rows := strings.Split(w1.Screen(), "\n"); len(rows) != 26 || len([]rune(rows[24])) > SideCols(87) && strings.TrimSpace(string([]rune(rows[24])[SideCols(87):])) != "" {
		t.Fatalf("the row above the status bar isn't empty:\n%s", w1.Screen())
	}
	env.AssertMirrorsServer(w1) // mid-stream
	w1.Detach()
	w1.WaitExit(wait)
	if !strings.Contains(w1.Screen(), "[detached from "+s.ID+"]") {
		t.Fatalf("window after detach:\n%s", w1.Screen())
	}
	if m := w1.Modes(); m.AltScreen || m.MouseTracking() || m.BracketedPaste {
		t.Errorf("outer terminal not restored after detach: %+v", m)
	}
	env.AssertAlive(s)

	// Another window, another size, still mid-stream; no session named:
	// the newest one.
	w2 := env.Attach(100, 32, "")
	w2.WaitFor("line", wait)
	env.AssertMirrorsServer(w2)
	w2.WaitFor("ready", 3*wait)
	env.AssertMirrorsServer(w2)
	// Attaching a pane a console sized never resizes it (docs/SPEC.md §3.3).
	assertPaneSize(t, env, s, 80, 24)
	if strings.Contains(env.Screen(s), "resized") {
		t.Fatalf("the program saw a resize:\n%s", env.Screen(s))
	}

	// A window of the pane's size, the sidebar's strip and the status
	// bar shows exactly the server's screen beside the strip.
	w3 := env.Attach(80+uint16(SideCols(87)), 24+statusRows, s.ID)
	w3.WaitFor("ready", wait)
	w3.Quiet(200 * time.Millisecond)
	if got, want := w3.PaneScreen(), env.Screen(s); got != want {
		t.Fatalf("outer screen differs from the server's:\n--- outer\n%s\n--- server\n%s", got, want)
	}
	w3.GoldenPane("attach-printer-80x24.txt")

	// Close both windows outright: the clients go, the session stays.
	w2.CloseWindow()
	w3.CloseWindow()
	w2.WaitExit(wait)
	w3.WaitExit(wait)
	env.AssertAlive(s)
	if !Poll(wait, func() bool {
		for _, info := range env.Sessions() {
			if info.ID == s.ID {
				return info.Clients == 0
			}
		}
		return false
	}) {
		t.Fatal("the server still counts clients after their windows closed")
	}
}

// TestSmokeAttachCloseWindowMidStream closes the window while a shell
// streams output as fast as it can, then reattaches.
func TestSmokeAttachCloseWindowMidStream(t *testing.T) {
	env := New(t)
	s := env.Start("shell")
	env.WaitFor(s, "$", wait)
	w := env.Attach(90, 30, s.ID)
	w.WaitFor("$", wait)
	w.Type("i=0; while [ $i -lt 20000 ]; do i=$((i+1)); echo row $i; done; echo stream-$((6*7))\r")
	w.WaitFor("row 1", wait)
	w.CloseWindow()
	w.WaitExit(wait)
	env.AssertAlive(s)

	w2 := env.Attach(80, 24, s.ID)
	w2.WaitFor("stream-42", 60*time.Second)
	env.AssertMirrorsServer(w2)
	// Typing in the first window gave the pane its size (less the
	// sidebar and the status bar); attaching w2 didn't change it
	// (docs/SPEC.md §3.3).
	assertPaneSize(t, env, s, uint16(90-SideCols(90)), 30-statusRows)
}

// TestAttachKillClient SIGKILLs the client mid-stream: the server
// drops it and carries on.
func TestAttachKillClient(t *testing.T) {
	env := New(t)
	s := env.Start("printer", "-lines", "3000", "-delay", "2ms")
	w := env.Attach(80, 24, s.ID)
	w.WaitFor("line ", wait)
	w.KillClient()
	env.AssertAlive(s)
	w2 := env.Attach(80, 24, s.ID)
	w2.WaitFor("ready", 3*wait)
	env.AssertMirrorsServer(w2)
}

// TestSmokeAttachFullscreenInput is M2's "run claude by hand" Try it,
// against the full-screen app: keys go through libghostty's encoders
// against the app's modes, mouse and focus modes are mirrored onto the
// window, Ctrl+B d detaches without either key reaching the app, and the window gets
// its terminal back.
func TestSmokeAttachFullscreenInput(t *testing.T) {
	env := New(t)
	s := env.Start("fullscreen")
	env.WaitFor(s, "fullscreen ready", wait)

	side := SideCols(87)
	w := env.Attach(80+uint16(side), 24+statusRows, s.ID)
	w.WaitFor("fullscreen ready", wait)
	if !Poll(wait, func() bool { m := w.Modes(); return m.AnyMouse && m.Focus && m.AltScreen && m.BracketedPaste }) {
		t.Fatalf("modes not mirrored onto the window: %+v", w.Modes())
	}
	// The window answered the client's colour-scheme query; the app asked
	// for reports (2031), so it gets one.
	w.WaitFor(`in: "\x1b[?997;1n"`, wait)

	w.Key(ShiftEnter)
	w.WaitFor(`in: "\x1b[13;2u"`, wait)
	w.Type("x")
	w.WaitFor(`in: "x"`, wait)
	w.Paste("one\ntwo")
	w.WaitFor(`in: "\x1b[200~one\ntwo\x1b[201~"`, wait)
	w.Wheel(true, side+4, 5) // the pane starts right of the sidebar
	w.WaitFor(`in: "\x1b[<64;5;6M"`, wait)
	env.AssertMirrorsServer(w)
	w.Quiet(200 * time.Millisecond)
	if got, want := w.PaneScreen(), env.Screen(s); got != want {
		t.Fatalf("outer screen differs from the server's:\n--- outer\n%s\n--- server\n%s", got, want)
	}
	w.GoldenPane("attach-fullscreen-80x24.txt")

	w.Detach()
	w.WaitExit(wait)
	if sc := env.Screen(s); strings.Contains(sc, `\x02`) || strings.Contains(sc, "98;5u") || strings.Contains(sc, `in: "d"`) {
		t.Fatalf("the prefix or its command reached the app:\n%s", sc)
	}
	if m := w.Modes(); m.AltScreen || m.MouseTracking() || m.Focus || m.BracketedPaste {
		t.Errorf("window not restored after detach: %+v", m)
	}
	env.AssertAlive(s)
}

// TestAttachResize: only a real window resize resizes the pane, and
// the mirror follows at the same point of the stream.
func TestAttachResize(t *testing.T) {
	env := New(t)
	s := env.Start("printer", "-lines", "5")
	env.WaitFor(s, "ready", wait)
	w := env.Attach(100, 30, s.ID)
	w.WaitFor("ready", wait)
	// The first window to show the pane fills it.
	waitPaneSize(t, env, s, paneCols(100), 30-statusRows)
	w.Resize(96, 20) // less the sidebar and the status bar
	resized := fmt.Sprintf("resized to %dx%d", paneCols(96), 20-statusRows)
	env.WaitFor(s, resized, wait)
	w.WaitFor(resized, wait)
	assertPaneSize(t, env, s, paneCols(96), 20-statusRows)
	env.AssertMirrorsServer(w)
}

// TestSmokeAttachLatestTypistResizes: two windows of different sizes on
// one pane. The first to show it fills it; watching from the other
// resizes nothing; typing in a window gives the pane that window's size,
// and typing in the other takes it back.
func TestSmokeAttachLatestTypistResizes(t *testing.T) {
	env := New(t)
	s := env.Start("printer", "-lines", "5")
	env.WaitFor(s, "ready", wait)
	w1 := env.Attach(100, 30, s.ID)
	w1.WaitFor("ready", wait)
	waitPaneSize(t, env, s, paneCols(100), 30-statusRows)
	w2 := env.Attach(90, 20, s.ID)
	w2.WaitFor("ready", wait)
	time.Sleep(time.Second) // longer than the server's resize quiet time
	assertPaneSize(t, env, s, paneCols(100), 30-statusRows)

	// Each window's panes are the window less the sidebar and the
	// status bar.
	small, big := fmt.Sprintf("resized to %dx%d", paneCols(90), 20-statusRows), fmt.Sprintf("resized to %dx%d", paneCols(100), 30-statusRows)
	w2.Type("a")
	env.WaitFor(s, small, wait)
	waitPaneSize(t, env, s, paneCols(90), 20-statusRows)
	w1.Type("b")
	env.WaitFor(s, big, wait)
	waitPaneSize(t, env, s, paneCols(100), 30-statusRows)
	w1.WaitFor(big, wait)
	w2.WaitFor(big, wait)
	env.AssertMirrorsServer(w1)
	env.AssertMirrorsServer(w2)
}

// TestAttachExplicitAgentKeepsSize: an agent whose manifest says
// screen.resize = "explicit" isn't resized by typing, only by a real
// window resize.
func TestAttachExplicitAgentKeepsSize(t *testing.T) {
	env := New(t)
	env.FakeClaude(func(m string) string { return m + "\n[screen]\nresize = \"explicit\"\n" })
	dir := env.Workdir()
	env.Trust(dir)
	s := env.StartAgent("claude", dir)
	env.WaitState(s, "idle", agentWait)
	w := env.Attach(90, 24, s.ID)
	w.Quiet(200 * time.Millisecond)
	w.Type("x")
	time.Sleep(time.Second) // longer than the server's resize quiet time
	assertPaneSize(t, env, s, 100, 30)
	w.Resize(85, 20)
	waitPaneSize(t, env, s, paneCols(85), 20-statusRows) // less the sidebar and the status bar
}

// TestAttachSlowClientResync stops a client while a firehose fills its
// queue: the server replaces the backlog with a snapshot, and the client
// comes back in sync.
func TestAttachSlowClientResync(t *testing.T) {
	env := New(t)
	s := env.Start("shell")
	env.WaitFor(s, "$", wait)
	w := env.Attach(80, 24, s.ID)
	w.WaitFor("$", wait)
	syscall.Kill(w.PID(), syscall.SIGSTOP)
	env.Keys(s, "i=0; while [ $i -lt 120000 ]; do i=$((i+1)); echo firehose-row-$i-padding-padding-padding; done; echo done-$((6*7))\r")
	env.WaitFor(s, "done-42", 120*time.Second)
	syscall.Kill(w.PID(), syscall.SIGCONT)
	w.WaitFor("done-42", wait)
	env.AssertMirrorsServer(w)
	resynced := false
	for _, l := range w.AttachLog() {
		resynced = resynced || strings.Contains(l, "resync")
	}
	if !resynced {
		t.Fatalf("the client was never resynced; log:\n%s", strings.Join(w.AttachLog(), "\n"))
	}
}

// TestAttachBuildMismatchReexec attaches with a different build of tm: it
// re-execs the server's binary and attaches anyway.
func TestAttachBuildMismatchReexec(t *testing.T) {
	env := New(t)
	s := env.Start("printer", "-lines", "3")
	env.WaitFor(s, "ready", wait)
	b, err := os.ReadFile(env.Bin)
	if err != nil {
		t.Fatal(err)
	}
	// Same program, different bytes: a different build id.
	other := filepath.Join(t.TempDir(), "tm-other")
	if err := os.WriteFile(other, append(b, "other build"...), 0o755); err != nil {
		t.Fatal(err)
	}
	w := env.WindowCmd(80, 24, other, "attach", s.ID)
	w.WaitFor("ready", wait)
	env.AssertMirrorsServer(w)
	reexec := false
	for _, l := range w.AttachLog() {
		reexec = reexec || strings.Contains(l, "re-exec "+filepath.Join(filepath.Dir(env.Socket), "bin"))
	}
	if !reexec {
		t.Fatalf("no re-exec logged; log:\n%s", strings.Join(w.AttachLog(), "\n"))
	}
}

// TestAttachLocalScrollback: Shift+PgUp scrolls the client's own view of a
// main-screen program; the next key goes back to the live screen.
func TestAttachLocalScrollback(t *testing.T) {
	env := New(t)
	s := env.Start("printer", "-lines", "60")
	env.WaitFor(s, "ready", wait)
	w := env.Attach(80+uint16(SideCols(87)), 24+statusRows, s.ID) // the pane's size: no resize
	w.WaitFor("ready", wait)
	w.Key(ShiftPageUp) // half a page
	w.WaitUntil("the view scrolled back", wait, func(string) bool {
		scr := w.PaneScreen()
		return strings.HasPrefix(scr, "line 27\n") && !strings.Contains(scr, "ready")
	})
	if !strings.Contains(env.Screen(s), "ready") {
		t.Fatalf("scrolling moved the server's view:\n%s", env.Screen(s))
	}
	w.Type("x")
	w.WaitFor("ready", wait)
}

// TestRunScript runs scripts/run.sh (`make run`) in a window: it opens
// the dashboard; s starts a shell and attaches; Ctrl+B d comes back to the
// dashboard and q leaves the shell running.
func TestRunScript(t *testing.T) {
	env := New(t)
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	w := env.WindowCmd(100, 30, filepath.Join(root, "scripts", "run.sh"))
	w.WaitFor("no sessions; s starts a shell", wait)
	w.Type("s")
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, `prefix+d dashboard`) && strings.Contains(sc, "$") })
	w.Type("echo hello-$((6*7))\r")
	w.WaitFor("hello-42", wait)
	w.Detach()
	w.WaitFor("SESSIONS", wait)
	w.Quit()
	w.WaitFor("keep running in the background server", wait)
	w.WaitExit(wait)
	if list := env.Sessions(); len(list) != 1 {
		t.Fatalf("sessions after quitting the dashboard: %+v", list)
	}
}

// TestRunScriptLoginShellQueries guards the `make run` path that once left
// a user with a blank pane (t-0011, cause not found): a server of another
// build is stopped first, then scripts/run.sh starts a server and an
// interactive login shell at the window's size and attaches right away,
// as `make run` does. The shell's prompt queries the terminal before
// every prompt (termquery, like starship), and the pane goes through
// detach/reattach cycles. The server's screen must keep showing output,
// every query must be answered, and every mirror must equal the server.
func TestRunScriptLoginShellQueries(t *testing.T) {
	env := New(t)
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	// An interactive bash login shell (zsh is not on every CI runner)
	// whose prompt runs termquery first.
	home := ""
	for _, kv := range env.Vars {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = v
		}
	}
	profile := "PS1='prompt$ '\nPROMPT_COMMAND=termquery\n"
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	// Without macOS bash's deprecation banner, the first thing on screen
	// is the prompt, which run.sh waits for before it types: keys typed
	// while termquery holds the terminal are swallowed, as with starship.
	env.Vars = append(env.Vars, "SHELL=/bin/bash", "BASH_SILENCE_DEPRECATION_WARNING=1")

	// A server of another build, with a session, stopped by this tm.
	b, err := os.ReadFile(env.Bin)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "tm-other")
	if err := os.WriteFile(other, append(b, "other build"...), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(other, "session", "start", "--cwd", "/", "--", "/bin/sh")
	cmd.Env = env.Vars
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("session on the other build: %v\n%s", err, out)
	}
	env.MustCLI("server", "stop")

	// make run, in a window the size of the user's: s starts the login
	// shell at the window's size, less the sidebar, the status bar and the row above it,
	// and attaches.
	w := env.WindowCmd(76, 53, filepath.Join(root, "scripts", "run.sh"))
	w.WaitFor("no sessions; s starts a shell", wait)
	w.Type("s")
	w.WaitFor("prompt$", wait)
	if !Poll(wait, func() bool { return w.Modes().AltScreen }) {
		t.Fatal("run.sh did not attach")
	}
	list := env.Sessions()
	if len(list) != 1 {
		t.Fatalf("sessions: %+v", list)
	}
	s := &Session{ID: list[0].ID, PID: list[0].PID}
	env.track(s.PID, "session "+s.ID)
	assertPaneSize(t, env, s, uint16(76-SideCols(76)), 51) // 76 is narrow: the sidebar is its slim strip
	// mirrored: w runs tm attach itself (not run.sh), so it can be asked
	// for a digest check.
	check := func(w *Window, marker string, mirrored bool) {
		t.Helper()
		w.Type("echo " + marker + "-$((6*7))\r")
		w.WaitFor(marker+"-42", wait)
		scr := env.WaitFor(s, marker+"-42", wait)
		if strings.Contains(scr, "q-timeout") || !strings.Contains(scr, "q-ok") {
			t.Fatalf("the prompt's terminal queries went unanswered:\n%s", scr)
		}
		if mirrored {
			env.AssertMirrorsServer(w)
		}
	}
	check(w, "first", false)
	w.Detach()
	w.WaitFor("SESSIONS", wait)
	w.Quit()
	w.WaitExit(wait)

	for i := range 4 {
		w = env.Window(76, 53, "attach", s.ID)
		w.WaitFor("prompt$", wait)
		check(w, fmt.Sprintf("cycle%d", i), true)
		w.Detach()
		w.WaitFor("[detached from "+s.ID+"]", wait)
		w.WaitExit(wait)
		env.AssertAlive(s)
	}
}

// sideDefault is the full sidebar's default width, its border included.
const sideDefault = view.SideDefault

// paneCols is a lone pane's width in a window cols wide: the window less
// the sidebar at the default layout.
func paneCols(cols int) uint16 { return uint16(cols - SideCols(cols)) }

// threadCols is the width of a thread's or a coordinator's pane in a
// window cols wide: less the sidebar and the info panel beside it.
func threadCols(cols int) uint16 { return besidePanel(cols, SideCols(cols)) }

// besidePanel is the width of a pane with the info panel (a thread's or
// a coordinator's) in a window cols wide beside a sidebar side wide.
func besidePanel(cols, side int) uint16 {
	return uint16(cols - side - view.Info{}.Cols(cols, side))
}

// waitPaneSize waits until session s is cols×rows: split panes resize
// their sessions in the background.
func waitPaneSize(t *testing.T, env *Env, s *Session, cols, rows uint16) {
	t.Helper()
	var c, r uint16
	if !Poll(wait, func() bool { c, r = paneSize(env, s); return c == cols && r == rows }) {
		t.Fatalf("%s is %d×%d, want %d×%d", s.ID, c, r, cols, rows)
	}
}

// TestSmokeAttachPane: the attach view is one pane beside the projects
// sidebar, with an empty row between it and the status bar: the pane
// gets the window less both, so nothing is cropped. Split panes and zoom
// are gone: their old prefix keys cancel. The prefix twice sends the
// prefix key itself.
func TestSmokeAttachPane(t *testing.T) {
	env := New(t)
	s1 := env.StartSize(120, 29, "shell")
	w := env.Window(120, 30)
	w.WaitFor(s1.ID+" ", wait)
	w.Key(Enter)
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, `prefix+d dashboard`) })
	w.Type("echo pane-$((2*3))\r")
	w.WaitFor("pane-6", wait)
	// The columns beside the sidebar, 28 rows above the empty row and
	// the status bar.
	waitPaneSize(t, env, s1, paneCols(120), 28)
	rows := strings.Split(w.Screen(), "\n")
	if r, side := []rune(rows[28]), SideCols(120); len(r) < side || strings.TrimSpace(string(r[side:])) != "" {
		t.Fatalf("the row above the status bar isn't empty:\n%s", w.Screen())
	}
	if !strings.Contains(rows[29], "prefix+d dashboard") {
		t.Fatalf("no status bar on the last row:\n%s", w.Screen())
	}
	for _, b := range []string{"│ ─", "⤢"} {
		if strings.Contains(rows[29], b) {
			t.Fatalf("the status bar has window buttons %q:\n%s", b, rows[29])
		}
	}

	// The old split and zoom keys cancel: no new session, the same size.
	for _, k := range []string{"%", `"`, "z", "x"} {
		w.Prefix(k)
	}
	w.Quiet(300 * time.Millisecond)
	if n := len(env.Sessions()); n != 1 {
		t.Fatalf("%d sessions after the old split keys", n)
	}
	assertPaneSize(t, env, s1, paneCols(120), 28)

	// The prefix twice sends Ctrl+B itself: od shows the byte, 002.
	w.Type("od -c\r")
	w.Quiet(300 * time.Millisecond)
	w.Key(CtrlB)
	w.Key(CtrlB)
	w.Type("\r\x04")
	w.WaitFor("002  \\n", wait)

	w.Detach()
	w.WaitFor("SESSIONS 1", wait)
	w.Quit()
	w.WaitExit(wait)
}
