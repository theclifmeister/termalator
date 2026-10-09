// Package flock is terminatr's one file lock: an advisory, exclusive,
// per-open-file lock that the kernel drops when its holder exits, however
// it exits. Callers: the server lock, doctor's probes, mdfile's sibling
// ".lock" files.
//
// Unix uses flock(2). Windows uses LockFileEx on one byte far past the
// end of the file (offset 1<<62), so the lock never blocks reads or
// writes of the file's contents; it is mandatory only against other
// LockFileEx calls. Inheritable and Inherited carry the lock across a
// spawn as an inheritable handle, whose value takes the place of the file
// descriptor number.
package flock
