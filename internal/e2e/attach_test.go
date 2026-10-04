package e2e

// M2 scenarios: tm attach as the user sees it (docs/SPEC.md §15 M2).
// TestSmoke* run on every PR; the rest nightly.

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
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
// program streams, detach with Ctrl+\, reattach from another window at
// another size, close that window mid-stream. The pane is never resized
// by attaching, nothing is lost, and every mirror equals the server.
func TestSmokeAttachDetachReattach(t *testing.T) {
	env := New(t)
	// About 6 s of output: long enough to still be streaming when the
	// second window attaches, even under the race detector.
	s := env.Start("printer", "-lines", "3000", "-delay", "2ms")

	w1 := env.Attach(120, 40, s.ID)
	w1.WaitFor("line ", wait)
	env.AssertMirrorsServer(w1) // mid-stream
	w1.Key(CtrlBackslash)
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
	// Attaching never resizes the pane (docs/SPEC.md §3.3).
	assertPaneSize(t, env, s, 80, 24)
	if strings.Contains(env.Screen(s), "resized") {
		t.Fatalf("the program saw a resize:\n%s", env.Screen(s))
	}

	// A window of the pane's size shows exactly the server's screen.
	w3 := env.Attach(80, 24, s.ID)
	w3.WaitFor("ready", wait)
	w3.Quiet(200 * time.Millisecond)
	if got, want := w3.Screen(), env.Screen(s); got != want {
		t.Fatalf("outer screen differs from the server's:\n--- outer\n%s\n--- server\n%s", got, want)
	}
	Golden(t, w3.Screen(), "attach-printer-80x24.txt")

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
	assertPaneSize(t, env, s, 80, 24)
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
// window, Ctrl+\ detaches without reaching the app, and the window gets
// its terminal back.
func TestSmokeAttachFullscreenInput(t *testing.T) {
	env := New(t)
	s := env.Start("fullscreen")
	env.WaitFor(s, "fullscreen ready", wait)

	w := env.Attach(80, 24, s.ID)
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
	w.Wheel(true, 4, 5)
	w.WaitFor(`in: "\x1b[<64;5;6M"`, wait)
	env.AssertMirrorsServer(w)
	w.Quiet(200 * time.Millisecond)
	if got, want := w.Screen(), env.Screen(s); got != want {
		t.Fatalf("outer screen differs from the server's:\n--- outer\n%s\n--- server\n%s", got, want)
	}
	Golden(t, w.Screen(), "attach-fullscreen-80x24.txt")

	w.Key(CtrlBackslash)
	w.WaitExit(wait)
	if strings.Contains(env.Screen(s), `\x1c`) || strings.Contains(env.Screen(s), "92;5u") {
		t.Fatalf("the detach key reached the app:\n%s", env.Screen(s))
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
	assertPaneSize(t, env, s, 80, 24)
	w.Resize(90, 20)
	env.WaitFor(s, "resized to 90x20", wait)
	w.WaitFor("resized to 90x20", wait)
	assertPaneSize(t, env, s, 90, 20)
	env.AssertMirrorsServer(w)
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
		reexec = reexec || strings.Contains(l, "re-exec "+env.Bin)
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
	w := env.Attach(80, 24, s.ID)
	w.WaitFor("ready", wait)
	w.Key(ShiftPageUp) // half a page
	w.WaitUntil("the view scrolled back", wait, func(scr string) bool {
		return strings.HasPrefix(scr, "line 27\n") && !strings.Contains(scr, "ready")
	})
	if !strings.Contains(env.Screen(s), "ready") {
		t.Fatalf("scrolling moved the server's view:\n%s", env.Screen(s))
	}
	w.Type("x")
	w.WaitFor("ready", wait)
}

// TestRunScript runs scripts/run.sh (`make run`) in a window: it starts a
// shell session and attaches to it; Ctrl+\ leaves it running.
func TestRunScript(t *testing.T) {
	env := New(t)
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	w := env.WindowCmd(100, 30, filepath.Join(root, "scripts", "run.sh"))
	w.WaitFor("hello from termalator session s-", wait)
	if !Poll(wait, func() bool { return w.Modes().AltScreen }) {
		t.Fatal("run.sh did not attach")
	}
	w.Key(CtrlBackslash)
	w.WaitFor("keeps running in the background server", wait)
	w.WaitExit(wait)
	if list := env.Sessions(); len(list) != 1 {
		t.Fatalf("sessions after detach: %+v", list)
	}
}
