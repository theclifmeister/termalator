//go:build unix

package fsx

import (
	"io/fs"
	"syscall"
)

// owner is the uid that owns fi's file.
func owner(fi fs.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
