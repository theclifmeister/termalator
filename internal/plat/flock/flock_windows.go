//go:build windows

package flock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// ErrLocked means another open file handle holds the lock.
var ErrLocked = errors.New("lock is held")

// Lock is an exclusive lock held on an open file. The zero value holds
// nothing.
type Lock struct {
	f *os.File
}

// The locked byte sits at offset 1<<62, far past any data, so ordinary
// reads and writes of the file are never blocked by it.
func lockRegion() *windows.Overlapped {
	return &windows.Overlapped{Offset: 0, OffsetHigh: 1 << 30}
}

func lockFile(h windows.Handle, flags uint32) error {
	return windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|flags, 0, 1, 0, lockRegion())
}

func unlockFile(h windows.Handle) error {
	return windows.UnlockFileEx(h, 0, 1, 0, lockRegion())
}

func isViolation(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING)
}

func take(path string, flags uint32) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(windows.Handle(f.Fd()), flags); err != nil {
		f.Close()
		if isViolation(err) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &Lock{f: f}, nil
}

// TryLock creates path if needed and takes the lock without waiting. It
// returns ErrLocked if another open file handle holds it.
func TryLock(path string) (*Lock, error) {
	return take(path, windows.LOCKFILE_FAIL_IMMEDIATELY)
}

// Wait creates path if needed and takes the lock, blocking until it is
// free.
func Wait(path string) (*Lock, error) {
	return take(path, 0)
}

// Held reports whether some process holds the lock on path. It never
// creates the file: a missing file is not held.
func Held(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := windows.Handle(f.Fd())
	if err := lockFile(h, windows.LOCKFILE_FAIL_IMMEDIATELY); err != nil {
		if isViolation(err) {
			return true, nil
		}
		return false, err
	}
	unlockFile(h)
	return false, nil
}

// Unlock releases the lock and closes its file. In a process that adopted
// the lock with Inherited it only closes the handle; the lock stays with
// the process that took it.
func (l *Lock) Unlock() {
	unlockFile(windows.Handle(l.f.Fd()))
	l.f.Close()
}

// Fd is the lock file's handle.
func (l *Lock) Fd() uintptr { return l.f.Fd() }

// Inheritable lets the lock's handle pass to a child process that is
// started with handle inheritance, and returns its value, for the new
// program to adopt with Inherited. If the start fails, call
// Uninheritable.
//
// Unlike a Unix flock, a Windows byte-range lock belongs to the process
// that took it: the child's handle names the locked file but does not
// keep the lock alive. The lock lasts until this process unlocks, closes
// the handle or exits, so the caller must stay alive (and keep the lock)
// for as long as the child runs, as proc.Exec's spawn-and-wait does.
func (l *Lock) Inheritable() (int, error) {
	h := windows.Handle(l.f.Fd())
	if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		return 0, err
	}
	return int(h), nil
}

// Uninheritable makes the handle private to this process again.
func (l *Lock) Uninheritable() {
	windows.SetHandleInformation(windows.Handle(l.f.Fd()), windows.HANDLE_FLAG_INHERIT, 0)
}

func fileID(h windows.Handle) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(h, &info)
	return info, err
}

// Inherited adopts a lock this process got across a spawn as handle fd.
// The handle must be the file at path, and the lock must be held (by the
// spawning process, see Inheritable); otherwise it returns nil and leaves fd alone.
func Inherited(fd int, path string) *Lock {
	if fd <= 0 {
		return nil
	}
	h := windows.Handle(fd)
	have, err := fileID(h)
	if err != nil {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	want, err := fileID(windows.Handle(f.Fd()))
	if err != nil || have.VolumeSerialNumber != want.VolumeSerialNumber ||
		have.FileIndexHigh != want.FileIndexHigh || have.FileIndexLow != want.FileIndexLow {
		return nil
	}
	// Byte-range locks belong to the handle, so a second handle must be
	// refused; if it is not, nothing holds the lock.
	if lockFile(windows.Handle(f.Fd()), windows.LOCKFILE_FAIL_IMMEDIATELY) == nil {
		unlockFile(windows.Handle(f.Fd()))
		return nil
	}
	windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, 0)
	return &Lock{f: os.NewFile(uintptr(h), path)}
}
