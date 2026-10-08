//go:build !unix

package proc

import (
	"errors"
	"os"
)

// Not yet ported: every call fails with errors.ErrUnsupported. The
// Windows port replaces this (doc in proc.go).

func Alive(pid int) bool                        { return false }
func Terminate(pid int) error                   { return errors.ErrUnsupported }
func Kill(pid int) error                        { return errors.ErrUnsupported }
func Exec(bin string, argv, env []string) error { return errors.ErrUnsupported }
func StartDetached(s Spec) (*os.Process, error) { return nil, errors.ErrUnsupported }
func Detached() bool                            { return false }
func Detach() error                             { return errors.ErrUnsupported }
