//go:build !unix && !windows

package flock

import (
	"errors"
	"os"
)

// Not ported to this platform: every call returns errors.ErrUnsupported.

var ErrLocked = errors.New("lock is held")

type Lock struct {
	f *os.File
}

func TryLock(path string) (*Lock, error)  { return nil, errors.ErrUnsupported }
func Wait(path string) (*Lock, error)     { return nil, errors.ErrUnsupported }
func Held(path string) (bool, error)      { return false, errors.ErrUnsupported }
func (l *Lock) Unlock()                   {}
func (l *Lock) Fd() uintptr               { return l.f.Fd() }
func (l *Lock) Inheritable() (int, error) { return 0, errors.ErrUnsupported }
func (l *Lock) Uninheritable()            {}
func Inherited(fd int, path string) *Lock { return nil }
