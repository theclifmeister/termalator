package proc

import "os/exec"

// Group makes cancelling cmd's context kill cmd's process and every
// process below it (Windows has no process groups: Kill takes the tree).
func Group(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return Kill(cmd.Process.Pid) }
}

// GroupAlive reports whether pid is alive. Once a tree's root has exited
// nothing names its descendants reliably (the root's pid may be reused),
// so they don't count.
func GroupAlive(pid int) bool { return Alive(pid) }

// KillGroup kills pid and every process below it (Kill).
func KillGroup(pid int) error { return Kill(pid) }
