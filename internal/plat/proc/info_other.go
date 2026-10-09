//go:build !darwin && !linux && !windows

package proc

import "errors"

// Not yet ported: other Unixes.

func Lookup(pid int) (Info, error) { return Info{}, errors.ErrUnsupported }
func List() ([]Info, error)        { return nil, errors.ErrUnsupported }
