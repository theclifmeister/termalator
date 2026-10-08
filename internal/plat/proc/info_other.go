//go:build !darwin && !linux

package proc

import "errors"

// Not yet ported: other Unixes, and Windows (doc in proc.go).

func Lookup(pid int) (Info, error) { return Info{}, errors.ErrUnsupported }
func List() ([]Info, error)        { return nil, errors.ErrUnsupported }
