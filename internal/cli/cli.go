package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"

	"github.com/theclifmeister/termalator/internal/proto"
	"github.com/theclifmeister/termalator/internal/server"
)

// Exit codes (docs/SPEC.md §6.3).
const (
	ExitOK      = 0
	ExitRefused = 1
	ExitUsage   = 2
	ExitIO      = 3
)

// Env is where a command reads and writes; tests substitute it.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// StdEnv is the process's own stdio.
func StdEnv() Env { return Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr} }

// fail prints err for command cmd and returns the exit code it maps to.
func (e Env) fail(cmd string, err error) int {
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

func (e Env) usage(cmd, msg string) int {
	fmt.Fprintf(e.Stderr, "tm %s: %s\n", cmd, msg)
	return ExitUsage
}

func (e Env) printJSON(v any) int {
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
