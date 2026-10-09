//go:build !unix && !windows

package pty

import "errors"

// Neither Unix nor Windows: nothing to start a console on.

func Start(argv []string, dir string, env []string, cols, rows uint16) (Console, error) {
	return nil, errors.ErrUnsupported
}

func StartTerminal(argv []string, dir string, env []string, cols, rows uint16) (Console, error) {
	return nil, errors.ErrUnsupported
}
