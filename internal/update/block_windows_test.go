//go:build windows

package update

import (
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// blockReplace makes path impossible to rename: a file held open without
// FILE_SHARE_DELETE until the test ends.
func blockReplace(t *testing.T, path string) {
	t.Helper()
	os.WriteFile(path, []byte("old"), 0o755)
	p, _ := windows.UTF16PtrFromString(path)
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(h) })
}
