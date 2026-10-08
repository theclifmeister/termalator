// Package pty runs a session's process on a pseudo-terminal: a Console is
// the terminal's master side plus the process tree behind it, which it
// resizes, inspects and stops. It also reads other processes' argv and
// parent (ProcArgs, ParentPID).
//
// It works on Unix: macOS and Linux fully, other Unixes without ProcArgs
// and ParentPID. Elsewhere every call returns errors.ErrUnsupported
// (pty_other.go) until the Windows port adds ConPTY, with a Job Object
// standing in for the process group.
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
	// (SIGWINCH).
	Resize(cols, rows uint16) error
	// Foreground is the job the terminal runs in front: the shell's
	// current command, or the shell itself (its process group id).
	Foreground() (pid int, err error)
	// PID is the process Start ran, the leader of its process group.
	PID() int
	// Stop ends the process tree: a hangup (SIGHUP to the group), then a
	// kill (SIGKILL) if it is still running after grace. It returns once
	// the process has exited (Wait returned) or after the kill, with an
	// error that says it had to kill.
	Stop(grace time.Duration) error
	// Wait waits for the process to exit and reaps it; call it once.
	// The error is the exec package's: nil for exit status 0.
	Wait() error
}
