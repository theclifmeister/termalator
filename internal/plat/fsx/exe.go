package fsx

import (
	"os"
	"path/filepath"
	"strings"
)

// asideInfix names a program file moved out of the way: "tm.exe.old-<utc>".
const asideInfix = ".old-"

// SwapIn puts the file at newBin in the place of the program or library at
// path, keeping path's mode, and returns a restore that undoes it (nil
// when nothing was kept).
//
// Unix replaces path with one rename: the old file's processes keep
// running it. Windows can't replace a file that is in use, but can rename
// it, so the old file moves aside first ("tm.exe.old-<utc>", removed by
// CleanAside once nothing runs it) and newBin takes its name. A failure
// at any step leaves path as it was; the error is the system's, for the
// caller to name the holders (proc.Holders).
func SwapIn(newBin, path string) (restore func() error, err error) {
	if fi, err := os.Stat(path); err == nil {
		if err := os.Chmod(newBin, fi.Mode().Perm()|0o100); err != nil {
			return nil, err
		}
	}
	aside, err := moveAside(path)
	if err != nil {
		return nil, err
	}
	if err := rename(newBin, path); err != nil {
		if aside != "" {
			rename(aside, path)
		}
		return nil, err
	}
	if aside == "" {
		return nil, nil
	}
	// Undo: move the new file out of the way (to newBin, so the caller's
	// cleanup finds it) and the old one back.
	return func() error {
		if err := rename(path, newBin); err != nil {
			return err
		}
		return rename(aside, path)
	}, nil
}

// CleanAside removes the files SwapIn moved aside from path, as far as
// nothing runs them. It reports how many are left.
func CleanAside(path string) (left int) {
	dir, base := filepath.Split(path)
	es, _ := os.ReadDir(filepath.Clean(dir + "."))
	for _, e := range es {
		if strings.HasPrefix(e.Name(), base+asideInfix) && os.Remove(filepath.Join(dir, e.Name())) != nil {
			left++
		}
	}
	return left
}
