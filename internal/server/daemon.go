package server

import (
	"io"
	"os"
	"path/filepath"
)

// OpenLog opens the rotating server log.
func OpenLog(p Paths) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(p.Log), 0o700); err != nil {
		return nil, err
	}
	return openRotating(p.Log)
}
