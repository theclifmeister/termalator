// Package pty runs a session's process on a pseudo-terminal: a Console is
// the terminal's master side plus the process tree behind it, which it
// resizes, inspects and stops. Other processes' argv and parent are
// plat/proc's (Lookup).
//
// It works on Unix, and on Windows over ConPTY with a Job Object standing
// in for the process group (pty_windows.go, conpty_windows.go).
package pty

import (
	"io"
	"time"
)

// Console is a running process on its own terminal.
type Console interface {
	// Read and Write are the terminal's output and input. Close
	// releases the master; it interrupts a pending Read.
	io.ReadWriteCloser
	// SetReadDeadline bounds the next Read, where the master supports
	// it (an error says it doesn't).
	SetReadDeadline(t time.Time) error
	// Resize sets the window size; the foreground job learns of it
	// (SIGWINCH; on Windows ConPTY repaints at the new size).
	Resize(cols, rows uint16) error
	// Foreground is the job the terminal runs in front: the shell's
	// current command, or the shell itself (its process group id; on
	// Windows the shell's newest child).
	Foreground() (pid int, err error)
	// PID is the process Start ran, the leader of its process group (on
	// Windows, the first process in its job).
	PID() int
	// Stop ends the process tree: a hangup (SIGHUP to the group; on
	// Windows, closing the console), then a kill (SIGKILL; terminating
	// the job) if it is still running after grace. It returns once
	// the process has exited (Wait returned) or after the kill, with an
	// error that says it had to kill.
	Stop(grace time.Duration) error
	// Wait waits for the process to exit and reaps it; call it once.
	// The error is the exec package's: nil for exit status 0.
	Wait() error
}
