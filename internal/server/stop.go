package server

import (
	"errors"
	"fmt"
	"syscall"

	"github.com/theclifmeister/termilator/internal/proto"
	"github.com/theclifmeister/termilator/internal/pty"
)

// Stopped says how Stop stopped a server.
type Stopped struct {
	PID int
	// Protocol is the server's, when it answered a hello (0 otherwise).
	Protocol int
	// Signalled is set when the server could not be asked and got SIGTERM.
	Signalled bool
}

// Stop asks the server for p to stop, whatever protocol it speaks
// (docs/SPEC.md §3.1, §3.3 Stopping across protocols). It returns once
// the server has agreed or been signalled; the caller waits for the lock
// and the process (WaitStopped, WaitExited).
//
//  1. A server this tm can talk to gets server.stop.
//  2. An older server refuses this tm's hello but sends its own: tm
//     redials claiming that protocol, which every server accepts for
//     control, and calls server.stop, which every protocol has.
//  3. A server that can't be asked at all (no usable hello, an unknown
//     method) gets SIGTERM, but only when it is this home's server: the
//     lock is held, the pid file names a live `tm server run` process,
//     and that pid matches the one the server's hello gave, if any. A
//     server shuts down on SIGTERM just as on server.stop: sessions are
//     recorded and agents are resumed by the next server.
//
// Without yes, a server that runs agent sessions is not stopped: steps 1
// and 2 return the server's refusal, step 3 refuses because it can't
// tell; both are *proto.Error with code ErrRefused. ErrNotRunning means
// there was nothing to stop.
func Stop(p Paths, yes bool) (Stopped, error) {
	c, err := Connect(p, false)
	if errors.Is(err, ErrNotRunning) {
		return Stopped{}, err
	}
	var verr *proto.MismatchError
	if errors.As(err, &verr) && verr.Server.Protocol > 0 && verr.Server.Protocol < proto.Protocol {
		c, err = dial(p, proto.KindControl, verr.Server.Protocol)
	}
	want := 0
	if verr != nil {
		want = verr.Server.PID
	}
	if err == nil {
		defer c.Close()
		st := Stopped{PID: c.Server.PID, Protocol: c.Server.Protocol}
		err := c.Call(proto.MethodServerStop, proto.ServerStopParams{Yes: yes}, nil)
		var perr *proto.Error
		if !errors.As(err, &perr) || perr.Code != proto.ErrUnknownMethod {
			return st, err
		}
		want = st.PID
	}
	pid, err := Terminate(p, want, yes)
	return Stopped{PID: pid, Signalled: true}, err
}

// Terminate sends SIGTERM to this home's server when it can't be asked
// to stop. want, if not 0, is the pid the server announced in its hello.
func Terminate(p Paths, want int, yes bool) (int, error) {
	lk, err := tryLock(p.Lock)
	if err == nil {
		lk.unlock()
		return 0, ErrNotRunning
	}
	if !errors.Is(err, ErrLocked) {
		return 0, err
	}
	pid := readPID(p.PID)
	switch {
	case !alive(pid):
		return pid, fmt.Errorf("the server lock is held but %s names no live process (%d); see %s", p.PID, pid, p.Log)
	case want != 0 && want != pid:
		return pid, fmt.Errorf("the server says it is pid %d but %s says %d; not signalling either", want, p.PID, pid)
	case !isServerProcess(pid):
		return pid, fmt.Errorf("pid %d in %s is not a tm server; not signalling it", pid, p.PID)
	case !yes:
		return pid, proto.Errorf(proto.ErrRefused,
			"the server (pid %d) can't be asked whether agents are working; pass --yes to stop it (agents are resumed)", pid)
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return pid, err
	}
	return pid, nil
}

// isServerProcess reports whether pid runs `tm server run`, whatever the
// binary is called (the server re-execs a pinned copy, §3.6).
func isServerProcess(pid int) bool {
	argv, err := pty.ProcArgs(pid)
	if err != nil {
		return false
	}
	return isServerArgv(argv)
}

func isServerArgv(argv []string) bool {
	return len(argv) >= 3 && argv[1] == "server" && argv[2] == "run"
}
