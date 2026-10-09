//go:build !unix && !windows

package fsx

import (
	"io/fs"
	"os"
)

// Other systems: nothing checked beyond the type, and the temp root is
// os.TempDir.

func openSync(path string) (*os.File, error)   { return os.Open(path) }
func rename(tmp, path string) error            { return os.Rename(tmp, path) }
func isLink(fi fs.FileInfo) bool               { return fi.Mode()&fs.ModeSymlink != 0 }
func checkOwnerMode(string, fs.FileInfo) error { return nil }
func TempRoots() []string                      { return []string{os.TempDir()} }
