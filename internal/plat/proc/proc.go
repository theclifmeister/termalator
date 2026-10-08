// Package proc is terminatr's view of other processes: whether one is
// alive, what it runs and who started it, stopping it, starting a
// detached one, turning this process into a daemon, and becoming another
// program.
//
// This file set is Unix only: kill(2), setsid(2), execve(2), with process
// details from sysctl on macOS and /proc on Linux (other Unixes have no
// Lookup or List yet). The Windows port adds files behind this API:
// OpenProcess and GetExitCodeProcess for Alive, NtQueryInformationProcess
// and Toolhelp32 for Lookup and List, CREATE_NO_WINDOW and
// CREATE_NEW_PROCESS_GROUP without inherited handles for StartDetached,
// and an Exec that runs the program on the same console, waits and exits
// with its code.
package proc

import (
	"errors"
	"os"
	"time"
)

// ErrNoProcess means no process has the pid.
var ErrNoProcess = errors.New("no such process")

// Info is what Lookup and List tell about a process. Argv and Exe are
// empty when the system won't say: another user's process, a zombie, a
// kernel thread, a child that hasn't exec'd yet.
type Info struct {
	PID, PPID int
	Argv      []string
	// Exe is the program's path as it was exec'd (macOS) or as the
	// kernel resolved it (Linux).
	Exe string
	// Started is when the process started; zero if unknown. On Linux it
	// is only as precise as the boot time (a second).
	Started time.Time
}

// ParentOf reports whether p is c's parent: c names p's pid as its parent
// and p did not start after c. A pid is reused once its process is gone,
// so a younger process with that pid is someone else (on Unix an orphan
// is handed to init, but Windows keeps the dead parent's pid).
func (p Info) ParentOf(c Info) bool {
	if p.PID != c.PPID {
		return false
	}
	return p.Started.IsZero() || c.Started.IsZero() || !p.Started.After(c.Started)
}

// Spec is a process for StartDetached.
type Spec struct {
	// Argv[0] is the program's path.
	Argv []string
	// Dir is its working directory; empty for this process's.
	Dir string
	// Env is its environment; nil for this process's.
	Env []string
	// Stdin, Stdout and Stderr are its standard files; nil for the null
	// device.
	Stdin, Stdout, Stderr *os.File
}
