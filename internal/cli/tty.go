package cli

import (
	"os"

	"github.com/theclifmeister/terminatr/internal/plat/term"
)

// rawMode reports whether f is a terminal with echo and line editing off.
func rawMode(f *os.File) bool { return term.RawMode(f) }

func isTTY(f *os.File) bool { return term.IsTerminal(f) }

// termSize returns the size of the terminal on stdout or stdin, if any.
func termSize() (cols, rows uint16, ok bool) {
	for _, f := range []*os.File{os.Stdout, os.Stdin} {
		if c, r, ok := term.Size(f); ok {
			return uint16(c), uint16(r), true
		}
	}
	return 0, 0, false
}
