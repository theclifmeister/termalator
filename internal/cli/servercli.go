package cli

// Glue between the server/session commands and the dispatcher in cli.go.
// These commands talk to the server; their errors carry proto error codes
// that srvFail maps onto the shared exit codes.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/server"
)

func init() {
	commands["server"] = runServer
	commands["session"] = runSession
}

func runServer(e *Env, args []string) error  { return codeErr(serverCmd(e, args)) }
func runSession(e *Env, args []string) error { return codeErr(sessionCmd(e, args)) }

// codeErr turns an exit code that was already reported into an error for
// Env.report.
func codeErr(code int) error {
	if code == ExitOK {
		return nil
	}
	return &exitError{code: code}
}

// srvFail prints err for command cmd and returns the exit code it maps to.
func (e *Env) srvFail(cmd string, err error) int {
	fmt.Fprintf(e.Stderr, "tm %s: %v\n", cmd, err)
	var perr *proto.Error
	var verr *proto.MismatchError
	switch {
	case errors.As(err, &verr):
		return ExitIO
	case errors.As(err, &perr):
		switch perr.Code {
		case proto.ErrBadParams:
			return ExitUsage
		case proto.ErrRefused, proto.ErrUnknownSession:
			return ExitRefused
		}
		return ExitIO
	}
	return ExitIO
}

func (e *Env) srvUsage(cmd, msg string) int {
	fmt.Fprintf(e.Stderr, "tm %s: %s\n", cmd, msg)
	return ExitUsage
}

func (e *Env) srvJSON(v any) int {
	enc := json.NewEncoder(e.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return ExitIO
	}
	return ExitOK
}

// connect opens a control connection, starting the server if needed.
func connect(autostart bool) (*server.Client, server.Paths, error) {
	p, err := server.ResolvePaths()
	if err != nil {
		return nil, p, err
	}
	c, err := server.Connect(p, autostart)
	return c, p, err
}

func isTTY(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	return err == nil
}

// termSize returns the size of the terminal on stdout or stdin, if any.
func termSize() (cols, rows uint16, ok bool) {
	for _, f := range []*os.File{os.Stdout, os.Stdin} {
		ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
		if err == nil && ws.Col > 0 && ws.Row > 0 {
			return ws.Col, ws.Row, true
		}
	}
	return 0, 0, false
}
