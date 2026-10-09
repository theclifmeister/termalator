//go:build unix

package e2e

import (
	"path/filepath"
	"testing"
)

// TestSmokeMakeRun: `make run` (scripts/run.sh) opens the dashboard,
// where a shell started from the command line in the current directory attaches.
func TestSmokeMakeRun(t *testing.T) {
	env := New(t)
	dir := env.Workdir()
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	env.Vars = append(env.Vars, "PATH="+filepath.Dir(env.Bin)+":/usr/bin:/bin")
	w := env.WindowCmd(100, 30, "/bin/sh", "-c", "cd "+dir+" && exec "+filepath.Join(root, "scripts", "run.sh"))
	id := env.StartShell(dir)
	w.WaitFor(id, wait)
	w.OpenSession(id)
	w.WaitUntil("attached", wait, func(sc string) bool { return lastLine(sc, `prefix+d dashboard`) })
	for _, s := range env.Sessions() {
		env.track(s.PID, "session "+s.ID)
		if real, _ := filepath.EvalSymlinks(dir); s.Cwd != dir && s.Cwd != real {
			t.Errorf("session cwd %s, want %s", s.Cwd, dir)
		}
	}
	w.Detach()
	w.WaitFor("SESSIONS", wait)
	w.Quit()
	w.WaitExit(wait)
}
