package server

import (
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

// IsSessionLeader reports whether this process leads its own session,
// which (with no controlling terminal) is what detachment needs.
func IsSessionLeader() bool {
	sid, err := unix.Getsid(0)
	return err == nil && sid == os.Getpid()
}

// Respawn re-execs this binary with the same arguments in a new session
// and returns once it started. `tm server run --detached` uses it when it
// was launched by hand from a shell: a process group leader cannot call
// setsid itself.
func Respawn() error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devnull.Close()
	cmd := exec.Command(bin, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// Detach finishes turning this process into a daemon (docs/SPEC.md §3.1):
// stdio on /dev/null, SIGHUP and SIGINT ignored, cwd "/", umask 077. The
// caller is already a session leader without a controlling terminal.
func Detach() error {
	if !IsSessionLeader() {
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
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT)
	go func() {
		for range sigs {
		}
	}()
	syscall.Umask(0o077)
	return os.Chdir("/")
}

// OpenLog opens the rotating server log.
func OpenLog(p Paths) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(p.Log), 0o700); err != nil {
		return nil, err
	}
	return openRotating(p.Log)
}
