package mdfile

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/theclifmeister/terminatr/internal/plat/flock"
)

const fence = "+++"

// Split separates TOML front matter from the body. A file without a
// leading "+++" line has no front matter. An opening fence without a
// closing one is an error.
func Split(data []byte) (front, body []byte, err error) {
	s := string(data)
	first, _, _ := strings.Cut(s, "\n")
	if strings.TrimRight(first, "\r") != fence {
		return nil, data, nil
	}
	start := len(first) + 1
	for off := start; off <= len(s); {
		line, _, found := strings.Cut(s[off:], "\n")
		next := off + len(line)
		if found {
			next++
		}
		if strings.TrimRight(line, "\r") == fence {
			return []byte(s[start:off]), []byte(s[next:]), nil
		}
		if !found {
			break
		}
		off = next
	}
	return nil, nil, errors.New("front matter: no closing +++ line")
}

// Join renders front matter and body. A nil front value gives a file with
// no front matter.
func Join(front any, body []byte) ([]byte, error) {
	var b bytes.Buffer
	if front != nil {
		b.WriteString(fence + "\n")
		if err := toml.NewEncoder(&b).Encode(front); err != nil {
			return nil, err
		}
		if !utf8.Valid(b.Bytes()) {
			// The encoder passes invalid UTF-8 through, and no decoder
			// could read it back; refuse rather than write it.
			return nil, errors.New("front matter: invalid UTF-8")
		}
		b.WriteString(fence + "\n")
	}
	b.Write(body)
	return b.Bytes(), nil
}

// Decode splits data and decodes the front matter into v.
func Decode(data []byte, v any) (body []byte, err error) {
	front, body, err := Split(data)
	if err != nil {
		return nil, err
	}
	if v != nil && front != nil {
		if _, err := toml.Decode(string(front), v); err != nil {
			return nil, fmt.Errorf("front matter: %w", err)
		}
	}
	return body, nil
}

// Read reads path and decodes its front matter into v.
func Read(path string, v any) (body []byte, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	body, err = Decode(data, v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return body, nil
}

// LockPath is the lock file of path: a hidden ".<name>.lock" beside it,
// so locks don't clutter folders that people and agents read.
func LockPath(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".lock")
}

// Lock takes an exclusive lock on LockPath(path) and returns its release
// function. It blocks until the lock is free.
func Lock(path string) (unlock func(), err error) {
	l, err := flock.Wait(LockPath(path))
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return l.Unlock, nil
}

// WriteAtomic writes data to a temp file in path's directory and renames
// it over path. The caller holds the lock, if it needs one.
func WriteAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Write locks path and atomically replaces it with front matter and body.
func Write(path string, front any, body []byte) error {
	data, err := Join(front, body)
	if err != nil {
		return err
	}
	unlock, err := Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	return WriteAtomic(path, data, 0o644)
}

// Update is a locked read-modify-write. fn gets the current contents (nil
// if the file doesn't exist) and returns the new contents; returning the
// same bytes, or an error, leaves the file untouched.
func Update(path string, fn func(old []byte) ([]byte, error)) error {
	unlock, err := Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	data, err := fn(old)
	if err != nil {
		return err
	}
	if old != nil && bytes.Equal(old, data) {
		return nil
	}
	return WriteAtomic(path, data, 0o644)
}

// Append locks path and appends data to it, creating the file if needed.
// It is for append-only logs such as JOURNAL.md, where rewriting the whole
// file on every line would be wasteful.
func Append(path string, data []byte) error {
	unlock, err := Lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
