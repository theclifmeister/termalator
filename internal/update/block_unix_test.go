//go:build !windows

package update

import (
	"os"
	"path/filepath"
	"testing"
)

// blockReplace makes path impossible to rename a file over: a directory
// with something in it.
func blockReplace(t *testing.T, path string) {
	t.Helper()
	os.Mkdir(path, 0o755)
	os.WriteFile(filepath.Join(path, "keep"), nil, 0o600)
}
