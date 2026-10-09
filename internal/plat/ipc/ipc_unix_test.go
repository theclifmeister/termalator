//go:build unix

package ipc

import (
	"os"
	"syscall"
	"testing"
)

// Dial's errors with no socket at the address and with one nobody
// accepts on.
var errNoSocket, errRefused error = syscall.ENOENT, syscall.ECONNREFUSED

// sockDir is a directory short enough for sockets (t.TempDir is too long
// on macOS).
func sockDir(t *testing.T) string {
	dir, err := os.MkdirTemp("/tmp", "tmipc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// checkPrivate fails unless only this user can connect to a.
func checkPrivate(t *testing.T, a Addr) {
	if st, err := os.Stat(string(a)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v, %v", st.Mode(), err)
	}
}
