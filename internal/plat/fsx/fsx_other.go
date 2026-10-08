//go:build !unix

package fsx

import (
	"io/fs"
	"os"
)

// Not yet ported: no owner check, and the temp root is os.TempDir. The
// Windows port compares the owner SID with the user's.

func owner(fi fs.FileInfo) (int, bool) { return 0, false }

func TempRoots() []string { return []string{os.TempDir()} }
