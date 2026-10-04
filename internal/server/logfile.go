package server

import (
	"fmt"
	"os"
	"sync"
)

// Log rotation: server.log is rotated at logMaxBytes and logKeep old files
// are kept (server.log.1 is the newest).
const (
	logMaxBytes = 10 << 20
	logKeep     = 3
)

// rotatingFile is an append-only log file that rotates by size.
type rotatingFile struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
	max  int64
}

func openRotating(path string) (*rotatingFile, error) {
	r := &rotatingFile{path: path, max: logMaxBytes}
	if err := r.open(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	r.f, r.size = f, fi.Size()
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size > 0 && r.size+int64(len(p)) > r.max {
		r.rotate()
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotate() {
	r.f.Close()
	for i := logKeep - 1; i >= 1; i-- {
		os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	os.Rename(r.path, r.path+".1")
	if err := r.open(); err != nil {
		// Keep logging somewhere rather than nowhere.
		r.f, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}
