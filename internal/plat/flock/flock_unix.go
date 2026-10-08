//go:build unix

package flock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// ErrLocked means another open file description holds the lock.
var ErrLocked = errors.New("lock is held")

// Lock is an exclusive lock held on an open file. The zero value holds
// nothing.
type Lock struct {
	f *os.File
}

func open(path string, flag int) (*os.File, error) {
	return os.OpenFile(path, flag, 0o600)
}

func take(path string, how int) (*Lock, error) {
	f, err := open(path, os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, err
	}
	for {
		err = unix.Flock(int(f.Fd()), how)
		if !errors.Is(err, unix.EINTR) {
			break
		}
	}
	if err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// TryLock creates path if needed and takes the lock without waiting. It
// returns ErrLocked if another open file description holds it.
func TryLock(path string) (*Lock, error) {
	return take(path, unix.LOCK_EX|unix.LOCK_NB)
}

// Wait creates path if needed and takes the lock, blocking until it is
// free.
func Wait(path string) (*Lock, error) {
	return take(path, unix.LOCK_EX)
}

// Held reports whether some process holds the lock on path. It never
// creates the file: a missing file is not held.
func Held(path string) (bool, error) {
	f, err := open(path, os.O_RDWR)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return true, nil
		}
		return false, err
	}
	unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return false, nil
}

// Unlock releases the lock and closes its file.
func (l *Lock) Unlock() {
	unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	l.f.Close()
}

// Fd is the lock file's descriptor.
func (l *Lock) Fd() uintptr { return l.f.Fd() }

// Inheritable lets the lock's descriptor survive an exec and returns it,
// for the new program to adopt with Inherited. If the exec fails, call
// Uninheritable.
func (l *Lock) Inheritable() (int, error) {
	fd := int(l.f.Fd())
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, 0); err != nil {
		return 0, err
	}
	return fd, nil
}

// Uninheritable closes the descriptor on exec again.
func (l *Lock) Uninheritable() {
	unix.CloseOnExec(int(l.f.Fd()))
}

// Inherited adopts a lock this process got across an exec as descriptor
// fd. The descriptor must be the file at path, and the lock it holds must
// be this process's own; otherwise it returns nil and leaves fd alone.
func Inherited(fd int, path string) *Lock {
	if fd < 3 {
		return nil
	}
	var st, ps unix.Stat_t
	if unix.Fstat(fd, &st) != nil || unix.Stat(path, &ps) != nil || st.Dev != ps.Dev || st.Ino != ps.Ino {
		return nil
	}
	if unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		return nil
	}
	unix.CloseOnExec(fd)
	return &Lock{f: os.NewFile(uintptr(fd), path)}
}
