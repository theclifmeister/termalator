//go:build unix

package proc

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// Group makes cmd, not yet started, lead a process group of its own, and
// makes cancelling cmd's context SIGKILL the whole group.
func Group(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return unix.Kill(-cmd.Process.Pid, unix.SIGKILL) }
}

// GroupAlive reports whether pid or any process of the group it leads is
// alive.
func GroupAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(-pid, 0)
	return Alive(pid) || err == nil || errors.Is(err, unix.EPERM)
}

// KillGroup SIGKILLs the group pid leads, and pid itself.
func KillGroup(pid int) error {
	if pid <= 0 {
		return ErrNoProcess
	}
	unix.Kill(-pid, unix.SIGKILL)
	return Kill(pid)
}
