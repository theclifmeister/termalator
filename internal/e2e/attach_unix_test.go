//go:build unix

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

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

// TestRunScript runs scripts/run.sh (`make run`) in a window: it opens
// the dashboard; a shell started from the command line attaches; Ctrl+B d comes back to the
// dashboard and q leaves the shell running.
func TestRunScript(t *testing.T) {
	env := New(t)
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	w := env.WindowCmd(100, 30, filepath.Join(root, "scripts", "run.sh"))
	id := env.StartShell(root)
	w.WaitFor(id, wait)
	w.OpenSession(id)
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
	cmd := exec.Command(other, "session", "start", "--cwd", rootDir, "--", "/bin/sh")
	cmd.Env = env.Vars
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("session on the other build: %v\n%s", err, out)
	}
	env.MustCLI("server", "stop")

	// make run, in a window the size of the user's: a login shell started
	// from the command line attaches at the window's size, less the
	// sidebar, the status bar and the row above it.
	w := env.WindowCmd(76, 53, filepath.Join(root, "scripts", "run.sh"))
	id := env.StartShell(rootDir, "/bin/bash", "-l")
	w.WaitFor(id, wait)
	w.OpenSession(id)
	w.WaitFor("prompt$", wait)
	if !Poll(wait, func() bool { return w.Modes().AltScreen }) {
		t.Fatal("run.sh did not attach")
	}
	list := env.Sessions()
	if len(list) != 1 {
		t.Fatalf("sessions: %+v", list)
	}
	s := &Session{ID: id}
	for _, l := range list {
		if l.ID == id {
			s.PID = l.PID
		}
	}
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
