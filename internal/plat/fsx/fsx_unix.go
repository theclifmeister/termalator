//go:build unix

package fsx

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// openSync opens path read-only, which fsync allows.
func openSync(path string) (*os.File, error) { return os.Open(path) }

// rename moves tmp over path.
func rename(tmp, path string) error { return os.Rename(tmp, path) }

// isLink reports whether fi is a symlink.
func isLink(fi fs.FileInfo) bool { return fi.Mode()&fs.ModeSymlink != 0 }

// checkOwnerMode fails unless the directory is owned by this user and
// closed to group and others.
func checkOwnerMode(dir string, fi fs.FileInfo) error {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s is owned by uid %d, not %d", dir, st.Uid, os.Getuid())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s has mode %04o, want 0700 (chmod 700 %s)", dir, fi.Mode().Perm(), dir)
	}
	return nil
}
