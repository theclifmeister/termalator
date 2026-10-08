// Package flock is terminatr's one file lock: an advisory, exclusive,
// per-open-file lock that the kernel drops when its holder exits, however
// it exits. Callers: the server lock, doctor's probes, mdfile's sibling
// ".lock" files.
//
// This file set is Unix only (flock(2)). The Windows port adds a file that
// uses LockFileEx on byte 0 of the same file, behind this API.
package flock
