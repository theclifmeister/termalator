package server

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func fileOwner(fi fs.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// ErrLocked means another process holds the server lock.
var ErrLocked = errors.New("server lock is held")

// lockFile is an exclusive flock on the server lock file. The kernel drops
// it when the holder exits, however it exits, so a crashed server never
// leaves a stale lock behind.
type lockFile struct {
	f *os.File
	// handedOver: this server came by exec into the pin (execPinned) and
	// must not exec again.
	handedOver bool
}

// tryLock takes the lock without waiting; it returns ErrLocked if another
// open file description holds it.
func tryLock(path string) (*lockFile, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return &lockFile{f: f}, nil
}

// lockWait bounds how long a starting server waits for a lock held by a
// process that isn't a running server: a client probing for a stale socket
// (Connect, WaitStopped, doctor) holds it for a moment, and a server that
// just took it has not yet written its pid file.
const lockWait = 2 * time.Second

// takeLock takes the server lock for a starting server, or keeps the one
// it held before its exec into the pin (execPinned). It refuses at once
// when the pid file names a live process, and otherwise retries until
// lockWait, so a probe that holds the lock for a moment is not taken for a
// server. It returns *AlreadyRunningError if the lock stays held.
func takeLock(p Paths) (*lockFile, error) {
	if lk := inheritedLock(p.Lock); lk != nil {
		return lk, nil
	}
	deadline := time.Now().Add(lockWait)
	for {
		lk, err := tryLock(p.Lock)
		if !errors.Is(err, ErrLocked) {
			return lk, err
		}
		pid := readPID(p.PID)
		if alive(pid) || time.Now().After(deadline) {
			return nil, &AlreadyRunningError{PID: pid}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (l *lockFile) unlock() {
	unix.Flock(int(l.f.Fd()), unix.LOCK_UN)
	l.f.Close()
}

// readPID reads a pid file; 0 if missing or malformed.
func readPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
