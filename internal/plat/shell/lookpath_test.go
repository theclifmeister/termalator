package shell

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestLookPathIn(t *testing.T) {
	files := fstest.MapFS{
		"bin/codex.cmd": {Mode: 0o644},
		"bin/tm.exe":    {Mode: 0o644},
		"bin/plain":     {Mode: 0o755},
		"bin/noexec":    {Mode: 0o644},
		"bin/dir.exe":   {Mode: fs.ModeDir | 0o755},
	}
	stat := func(p string) (fs.FileInfo, error) {
		return fs.Stat(files, filepath.ToSlash(p))
	}
	path := "other" + string(os.PathListSeparator) + "bin"
	tests := []struct {
		goos, ext, cmd, want string
		ok                   bool
	}{
		{"windows", ".EXE;.CMD", "codex", "bin/codex.cmd", true},
		{"windows", "", "tm", "bin/tm.exe", true},
		{"windows", ".EXE;.CMD", "tm.exe", "bin/tm.exe", true},
		{"windows", ".EXE", "codex", "", false},
		{"windows", "", "dir", "", false},
		{"windows", "", `C:\x\y.exe`, `C:\x\y.exe`, true},
		{"linux", "", "plain", "bin/plain", true},
		{"linux", "", "noexec", "", false},
		{"linux", "", "tm", "", false},
		{"linux", "", "/usr/bin/x", "/usr/bin/x", true},
	}
	for _, tc := range tests {
		got, err := lookPathIn(tc.goos, tc.ext, stat, tc.cmd, path)
		if (err == nil) != tc.ok || filepath.ToSlash(got) != filepath.ToSlash(tc.want) {
			t.Errorf("%s %q ext %q: got %q, %v", tc.goos, tc.cmd, tc.ext, got, err)
		}
	}
}
