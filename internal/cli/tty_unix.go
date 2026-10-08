//go:build unix

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// rawMode reports whether f is a terminal with echo and line editing off.
func rawMode(f *os.File) bool {
	t, err := unix.IoctlGetTermios(int(f.Fd()), ioctlGetTermios)
	return err == nil && t.Lflag&(unix.ICANON|unix.ECHO) == 0
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
