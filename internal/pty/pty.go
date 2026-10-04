// Package pty starts processes on a pseudo-terminal and handles resize and
// reaping (macOS and Linux only).
package pty

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	cpty "github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// Start runs argv in dir on a new PTY of the given size. The child becomes
// the leader of a new session with the PTY as its controlling terminal, so
// signalling its process group (-pid) reaches everything it started.
//
// The returned master is non-blocking and owned by Go's poller: Close
// interrupts a pending Read, and read deadlines work.
func Start(argv []string, dir string, env []string, cols, rows uint16) (*exec.Cmd, *os.File, error) {
	if len(argv) == 0 {
		return nil, nil, fmt.Errorf("pty: empty command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	orig, err := cpty.StartWithSize(cmd, &cpty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, nil, fmt.Errorf("pty: start %s: %w", argv[0], err)
	}
	// creack/pty calls Fd() on the master, which switches it to blocking
	// mode; Go then can't interrupt a Read on Close. Swap in a duplicate
	// that the poller owns.
	nfd, err := syscall.Dup(int(orig.Fd()))
	if err == nil {
		syscall.CloseOnExec(nfd)
		err = syscall.SetNonblock(nfd, true)
	}
	orig.Close()
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, nil, fmt.Errorf("pty: %w", err)
	}
	return cmd, os.NewFile(uintptr(nfd), "ptmx"), nil
}

// Resize sets the PTY window size; the kernel sends SIGWINCH to the
// foreground process group. It avoids f.Fd(), which would put the master
// back into blocking mode.
func Resize(f *os.File, cols, rows uint16) error {
	raw, err := f.SyscallConn()
	if err != nil {
		return fmt.Errorf("pty: resize: %w", err)
	}
	var serr error
	err = raw.Control(func(fd uintptr) {
		serr = unix.IoctlSetWinsize(int(fd), unix.TIOCSWINSZ, &unix.Winsize{Col: cols, Row: rows})
	})
	if err == nil {
		err = serr
	}
	if err != nil {
		return fmt.Errorf("pty: resize: %w", err)
	}
	return nil
}
