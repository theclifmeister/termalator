package fsx

import "os"

// TempRoots are the directories that hold temporary files: os.TempDir
// (the per-user folder under /var/folders) and /tmp, each also by its
// real name under /private.
func TempRoots() []string {
	return []string{os.TempDir(), "/tmp", "/private/tmp", "/var/folders", "/private/var/folders"}
}
