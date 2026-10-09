package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// testExe is a program file's extension.
const testExe = ".exe"

// chmodApplies: Windows ignores a directory's mode bits.
const chmodApplies = false

// testSh is the POSIX shell sessions run in tests: sh on PATH, else Git
// for Windows'; the test skips without one.
func testSh(t testing.TB) string {
	t.Helper()
	if p, err := exec.LookPath("sh"); err == nil {
		return p
	}
	for _, dir := range []string{os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA") + `\Programs`} {
		if p := filepath.Join(dir, "Git", "usr", "bin", "sh.exe"); dir != "" {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	t.Skip("no POSIX sh: install Git for Windows")
	return ""
}
