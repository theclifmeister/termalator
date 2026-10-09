//go:build !unix && !windows

package proc

import (
	"errors"
	"os"
)

// Not ported (neither Unix nor Windows): every call fails with
// errors.ErrUnsupported.

func Alive(pid int) bool                        { return false }
func Terminate(pid int) error                   { return errors.ErrUnsupported }
func Kill(pid int) error                        { return errors.ErrUnsupported }
func Exec(bin string, argv, env []string) error { return errors.ErrUnsupported }
func StartDetached(s Spec) (*os.Process, error) { return nil, errors.ErrUnsupported }
func Detached() bool                            { return false }
func Detach() error                             { return errors.ErrUnsupported }
