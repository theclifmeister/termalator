package server

import "golang.org/x/sys/unix"

// cloneFile makes dst an APFS clone of src: a file of its own (not a
// hard link, whose path macOS may report by the other name), sharing
// src's blocks until either is written.
func cloneFile(src, dst string) error {
	return unix.Clonefile(src, dst, 0)
}
