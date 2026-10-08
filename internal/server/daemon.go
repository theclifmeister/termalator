package server

import (
	"io"
	"os"
	"path/filepath"

	"github.com/theclifmeister/terminatr/internal/plat/proc"
)

// OpenLog opens the rotating server log.
func OpenLog(p Paths) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(p.Log), 0o700); err != nil {
		return nil, err
	}
	return openRotating(p.Log)
}

// Respawn starts this binary again with the same arguments, detached
// (proc.StartDetached), and returns once it started. `tm server run
// --detached` uses it when it was launched by hand from a shell, which
// can't become a daemon in place (proc.Detached).
func Respawn() error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	p, err := proc.StartDetached(proc.Spec{Argv: append([]string{bin}, os.Args[1:]...)})
	if err != nil {
		return err
	}
	return p.Release()
}
