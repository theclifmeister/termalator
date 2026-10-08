//go:build !unix

package pty

import "errors"

// Not yet ported: the Windows port adds ConPTY (doc in pty.go).

func Start(argv []string, dir string, env []string, cols, rows uint16) (Console, error) {
	return nil, errors.ErrUnsupported
}
