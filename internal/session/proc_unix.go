//go:build unix

package session

import (
	"errors"
	"syscall"
)

// gone reports whether no process has pid any more.
func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
