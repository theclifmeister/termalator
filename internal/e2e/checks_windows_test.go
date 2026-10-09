package e2e

import (
	"os"
	"testing"

	"github.com/theclifmeister/terminatr/internal/plat/proc"
)

// politeTerminate: proc.Terminate kills (Windows has no polite stop for
// a process it didn't start).
const politeTerminate = false

// passesReports: ConPTY's input parser drops a terminal report the window
// sends (CSI ? 997 ; 1 n) before it reaches the program (seen on Windows
// 11 ARM64 with the shipped OpenConsole 1.25).
const passesReports = false

// fallbackRunDirs holds a server's run dir when its home is too long.
var fallbackRunDirs = os.TempDir()

// assertOwnSession: Windows has no sessions to lead.
func assertOwnSession(t *testing.T, spid int) {}

// assertDetached checks what Windows can tell of the server's detachment
// from the window that started it: it is not the window's child (it was
// started detached, outside the window's job).
func assertDetached(t *testing.T, spid, windowPID int, socket string) {
	t.Helper()
	if info, err := proc.Lookup(spid); err == nil && info.PPID == windowPID {
		t.Errorf("server %d is the window's child", spid)
	}
	if _, err := os.Stat(socket); err != nil {
		t.Errorf("socket: %v", err)
	}
}

// replaceExe renames src over the program dst, as an upgrade does: a
// running exe can't be replaced, only renamed aside first.
func replaceExe(src, dst string) error {
	if err := os.Rename(dst, dst+".old"); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

// terminalSignals: a Windows console sends a detached process nothing.
func terminalSignals(pid int) {}
