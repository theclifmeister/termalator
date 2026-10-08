//go:build unix

package pty

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	cpty "github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// console is a process on a PTY master that Go's poller owns.
type console struct {
	*os.File // the master: Read, Write, Close, SetReadDeadline
	cmd      *exec.Cmd
	exited   chan struct{} // closed when Wait returns
}

// Start runs argv in dir on a new PTY of the given size. The child becomes
// the leader of a new session with the PTY as its controlling terminal, so
// signalling its process group (-pid) reaches everything it started.
//
// The master is non-blocking and owned by Go's poller: Close interrupts a
// pending Read, and read deadlines work.
func Start(argv []string, dir string, env []string, cols, rows uint16) (Console, error) {
	cmd, f, err := start(argv, dir, env, cols, rows)
	if err != nil {
		return nil, err
	}
	return &console{File: f, cmd: cmd, exited: make(chan struct{})}, nil
}

func (c *console) PID() int { return c.cmd.Process.Pid }

func (c *console) Resize(cols, rows uint16) error { return resize(c.File, cols, rows) }

func (c *console) Foreground() (int, error) { return foreground(c.File) }

func (c *console) Wait() error {
	err := c.cmd.Wait()
	close(c.exited)
	return err
}

func (c *console) Stop(grace time.Duration) error {
	pid := c.cmd.Process.Pid
	signal(pid, syscall.SIGHUP)
	select {
	case <-c.exited:
		return nil
	case <-time.After(grace):
	}
	signal(pid, syscall.SIGKILL)
	return fmt.Errorf("still running %v after SIGHUP; sent SIGKILL", grace)
}

// signal sends sig to pid's process group and to pid itself.
func signal(pid int, sig syscall.Signal) {
	syscall.Kill(-pid, sig)
	syscall.Kill(pid, sig)
}

func start(argv []string, dir string, env []string, cols, rows uint16) (*exec.Cmd, *os.File, error) {
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

// resize sets the PTY window size; the kernel sends SIGWINCH to the
// foreground process group. It avoids f.Fd(), which would put the master
// back into blocking mode.
func resize(f *os.File, cols, rows uint16) error {
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

// foreground returns the foreground process group of the PTY: the job a
// shell is running, or the shell itself.
func foreground(f *os.File) (int, error) {
	raw, err := f.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pgrp int
	var serr error
	err = raw.Control(func(fd uintptr) {
		pgrp, serr = unix.IoctlGetInt(int(fd), unix.TIOCGPGRP)
	})
	if err == nil {
		err = serr
	}
	return pgrp, err
}
