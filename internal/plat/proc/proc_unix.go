//go:build unix

package proc

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

// Alive reports whether a process has pid, whoever owns it.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := unix.Kill(pid, 0)
	return err == nil || errors.Is(err, unix.EPERM)
}

// Terminate asks pid to stop (SIGTERM). Callers ask over the control
// socket first: Windows has no polite stop for a process it didn't start.
func Terminate(pid int) error {
	return signal1(pid, unix.SIGTERM)
}

// Kill stops pid at once (SIGKILL).
func Kill(pid int) error {
	return signal1(pid, unix.SIGKILL)
}

// signal1 sends sig to the one process pid; never to a group or to every
// process, which kill(2) does for pid 0 and below.
func signal1(pid int, sig unix.Signal) error {
	if pid <= 0 {
		return fmt.Errorf("signal pid %d: %w", pid, ErrNoProcess)
	}
	err := unix.Kill(pid, sig)
	if errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("signal pid %d: %w", pid, ErrNoProcess)
	}
	return err
}

// Exec runs bin in place of this process, with argv (argv[0] included)
// and env. It returns only on failure.
func Exec(bin string, argv, env []string) error {
	return syscall.Exec(bin, argv, env)
}

// StartDetached starts s with no controlling terminal, in a new session,
// so it outlives this process and its terminal. Wait on the process to
// learn whether it exited early, or Release it.
func StartDetached(s Spec) (*os.Process, error) {
	if len(s.Argv) == 0 {
		return nil, errors.New("start: no program")
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer devnull.Close()
	or := func(f *os.File) *os.File {
		if f == nil {
			return devnull
		}
		return f
	}
	cmd := exec.Command(s.Argv[0], s.Argv[1:]...)
	cmd.Dir, cmd.Env = s.Dir, s.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = or(s.Stdin), or(s.Stdout), or(s.Stderr)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}

// Detached reports whether this process can become a daemon in place: it
// leads its own session (with no controlling terminal, as StartDetached
// leaves it). A process group leader, which a shell makes of every
// command, cannot start a session itself: it has to StartDetached a copy.
func Detached() bool {
	sid, err := unix.Getsid(0)
	return err == nil && sid == os.Getpid()
}

// Detach finishes turning this process into a daemon (docs/SPEC.md §3.1):
// stdio on the null device, SIGHUP and SIGINT ignored, cwd "/", umask 077.
// The caller is Detached.
func Detach() error {
	if !Detached() {
		if _, err := unix.Setsid(); err != nil {
			return err
		}
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	for fd := 0; fd <= 2; fd++ {
		if err := unix.Dup2(int(devnull.Fd()), fd); err != nil {
			return err
		}
	}
	// Catch and drop SIGHUP and SIGINT rather than signal.Ignore them: an
	// ignored disposition survives exec, so every session would inherit
	// it, and a shell that ignores SIGHUP cannot be stopped politely.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, unix.SIGHUP, unix.SIGINT)
	go func() {
		for range sigs {
		}
	}()
	unix.Umask(0o077)
	return os.Chdir("/")
}
