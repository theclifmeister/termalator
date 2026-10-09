//go:build unix

package e2e

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// politeTerminate: proc.Terminate asks a process to stop (SIGTERM).
const politeTerminate = true

// passesReports: a terminal report the window sends (CSI ? 997 ; 1 n)
// reaches the program in the pane.
const passesReports = true

// fallbackRunDirs holds a server's run dir when its home is too long.
const fallbackRunDirs = "/tmp"

// controllingTTY returns ps's tty column for pid ("?" or "??" for none).
func controllingTTY(t *testing.T, pid int) string {
	t.Helper()
	out, err := exec.Command("ps", "-o", "tty=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// assertOwnSession checks that the server leads a session of its own
// (setsid).
func assertOwnSession(t *testing.T, spid int) {
	t.Helper()
	if sid, _ := unix.Getsid(spid); sid != spid {
		t.Errorf("server sid %d, want %d (setsid)", sid, spid)
	}
}

// assertDetached checks the server's full detachment from the window
// that started it: its own session, no controlling terminal, stdio on
// /dev/null, a private socket.
func assertDetached(t *testing.T, spid, windowPID int, socket string) {
	t.Helper()
	assertOwnSession(t, spid)
	if wsid, _ := unix.Getsid(windowPID); wsid == spid {
		t.Error("server shares the window's session")
	}
	if tty := controllingTTY(t, spid); tty != "?" && tty != "??" {
		t.Errorf("server has controlling tty %q", tty)
	}
	if runtime.GOOS == "linux" {
		for _, fd := range []string{"0", "1", "2"} {
			if target, _ := os.Readlink("/proc/" + strconv.Itoa(spid) + "/fd/" + fd); target != os.DevNull {
				t.Errorf("server fd %s -> %q, want /dev/null", fd, target)
			}
		}
	}
	if fi, err := os.Stat(socket); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode: %v %v", fi, err)
	}
}

// replaceExe renames src over the program dst, as an upgrade does.
func replaceExe(src, dst string) error { return os.Rename(src, dst) }

// terminalSignals sends pid the signals a terminal or a careless user
// might send.
func terminalSignals(pid int) {
	syscall.Kill(pid, syscall.SIGHUP)
	syscall.Kill(pid, syscall.SIGINT)
}
