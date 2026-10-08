//go:build unix && !darwin

package fsx

import "os"

// TempRoots are the directories that hold temporary files: os.TempDir
// ($TMPDIR) and /tmp.
func TempRoots() []string {
	return []string{os.TempDir(), "/tmp"}
}
