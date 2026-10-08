// Package pty starts processes on a pseudo-terminal and handles resize and
// reaping. It works on Unix: macOS and Linux fully, other Unixes without
// ProcArgs and ParentPID. Elsewhere every call returns
// errors.ErrUnsupported (pty_other.go) until the Windows port adds ConPTY.
package pty
