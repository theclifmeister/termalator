//go:build !unix

package cli

import "os"

// Not yet ported: no terminal is recognised (plat/term will add the
// Windows console).

func rawMode(f *os.File) bool                { return false }
func isTTY(f *os.File) bool                  { return false }
func termSize() (cols, rows uint16, ok bool) { return 0, 0, false }
