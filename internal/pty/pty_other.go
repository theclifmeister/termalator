//go:build !unix

package pty

import (
	"errors"
	"os"
	"os/exec"
)

func Start(argv []string, dir string, env []string, cols, rows uint16) (*exec.Cmd, *os.File, error) {
	return nil, nil, errors.ErrUnsupported
}

func Resize(f *os.File, cols, rows uint16) error { return errors.ErrUnsupported }

func Foreground(f *os.File) (int, error) { return 0, errors.ErrUnsupported }
