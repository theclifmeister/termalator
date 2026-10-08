//go:build unix

package session

import (
	"errors"
	"syscall"
)

// hangup sends SIGHUP to the session's process group and to pid itself.
func hangup(pid int) {
	syscall.Kill(-pid, syscall.SIGHUP)
	syscall.Kill(pid, syscall.SIGHUP)
}

// kill sends SIGKILL to the session's process group and to pid itself.
func kill(pid int) {
	syscall.Kill(-pid, syscall.SIGKILL)
	syscall.Kill(pid, syscall.SIGKILL)
}

// gone reports whether no process has pid any more.
func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
